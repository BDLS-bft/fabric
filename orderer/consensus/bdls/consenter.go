package bdls

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"

	// "reflect"
	// "time"

	"code.cloudfoundry.org/clock"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"google.golang.org/protobuf/proto"

	//"github.com/go-viper/mapstructure/v2"
	//"github.com/hyperledger/fabric-config/configtx/orderer"
	"github.com/hyperledger/fabric-lib-go/bccsp"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-lib-go/common/metrics"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"

	//"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	ab "github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	//"github.com/hyperledger/fabric/common/channelconfig"
	//"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/common/policies"
	"github.com/hyperledger/fabric/internal/pkg/comm"
	"github.com/hyperledger/fabric/orderer/common/cluster"
	"github.com/hyperledger/fabric/orderer/common/localconfig"
	"github.com/hyperledger/fabric/orderer/common/multichannel"
	"github.com/hyperledger/fabric/orderer/consensus"

	//"github.com/hyperledger/fabric/orderer/consensus/bdls/util"
	//"github.com/hyperledger/fabric/orderer/consensus/bdls/wal"
	// "github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	//"go.uber.org/zap"
	//"google.golang.org/protobuf/proto"
)

// ChainGetter obtains instances of ChainSupport for the given channel
type ChainGetter interface {
	// GetChain obtains the ChainSupport for the given channel.
	// Returns nil, false when the ChainSupport for the given channel
	// isn't found.
	GetChain(chainID string) *multichannel.ChainSupport
}

// PolicyManagerRetriever retrieves a PolicyManager for a given channel.
// If this type already exists in your codebase, import it from the correct package instead.
type PolicyManagerRetriever func(channelID string) policies.Manager

// Consenter implementation of the BFT smart based consenter
type Consenter struct {
	CreateChain      func(chainName string)
	GetPolicyManager PolicyManagerRetriever
	Logger           *flogging.FabricLogger
	Identity         []byte
	Comm             *cluster.AuthCommMgr
	Chains           ChainGetter
	SignerSerializer SignerSerializer
	Registrar        *multichannel.Registrar
	//WALBaseDir       string
	ClusterDialer *cluster.PredicateDialer
	Conf          *localconfig.TopLevel
	Metrics       *Metrics
	//MetricsBFT       *api.Metrics
	//MetricsWalBFT    *wal.Metrics
	BCCSP          bccsp.BCCSP
	ClusterService *cluster.ClusterService
	// Some fields from etcdraft that i feel are important for BDLS
	Communication cluster.Communicator
	TLSCert       []byte
	Dispatcher    *Dispatcher
	OrdererConfig localconfig.TopLevel
}

// TargetChannel implements cluster.ChannelExtractor interface.
func (c *Consenter) TargetChannel(message proto.Message) string {
	switch req := message.(type) {
	case *ab.ConsensusRequest:
		return req.Channel
	case *ab.SubmitRequest:
		return req.Channel
	default:
		return ""
	}
}

func (c *Consenter) ReceiverByChain(channelID string) MessageReceiver {
	// TODO: Implement the logic to return the MessageReceiver for the given channelID
	// Return nil or an appropriate implementation of MessageReceiver
	return nil
}

// New creates Consenter of type bdls
func New(
	pmr PolicyManagerRetriever,
	signerSerializer SignerSerializer,
	clusterDialer *cluster.PredicateDialer,
	conf *localconfig.TopLevel,
	srvConf comm.ServerConfig, // TODO why is this not used?
	srv *comm.GRPCServer,
	r *multichannel.Registrar,
	metricsProvider metrics.Provider,
	clusterMetrics *cluster.Metrics,
	BCCSP bccsp.BCCSP,
) *Consenter {
	// TODO: Implement the logic to create a new Consenter instance
	// Initialize and return a new Consenter instance
	logger := flogging.MustGetLogger("orderer.consensus.bdls.New")

	logger.Debugf("*************Creating a new BDLS Consenter****************************")

	consenter := &Consenter{
		Registrar:        r,
		GetPolicyManager: pmr,
		Conf:             conf,
		ClusterDialer:    clusterDialer,
		Logger:           logger,
		Chains:           r,
		SignerSerializer: signerSerializer,
		//WALBaseDir:       walConfig.WALDir,
		Metrics: NewMetrics(metricsProvider),
		// MetricsBFT:       api.NewMetrics(mpc, "channel"),
		// MetricsWalBFT:    wal.NewMetrics(mpc, "channel"),
		CreateChain: r.CreateChain,
		BCCSP:       BCCSP,
		// from etcdraft
		TLSCert:       srvConf.SecOpts.Certificate,
		OrdererConfig: *conf,
	}

	// This will print the standard -----BEGIN CERTIFICATE----- block
	logger.Debugf("TLS Cert PEM:\n%s", srvConf.SecOpts.Certificate)

	consenter.Dispatcher = &Dispatcher{
		Logger:        logger,
		ChainSelector: consenter,
	}

	identity, _ := signerSerializer.Serialize()
	sID := &msp.SerializedIdentity{}
	if err := proto.Unmarshal(identity, sID); err != nil {
		logger.Panicf("failed unmarshaling identity: %s", err)
	}

	block, _ := pem.Decode(sID.IdBytes)
	if block == nil {
		logger.Warningf("Failed to decode identity certificate PEM for MSP: %s", sID.Mspid)
	} else {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			logger.Warningf("Failed to parse identity certificate for MSP %s: %v", sID.Mspid, err)
		} else {
			// Log the structured, human-readable identity information
			logger.Infof(
				"Loaded Consenter Identity | MSP: %s, Subject: %s, Expires: %v",
				sID.Mspid,
				cert.Subject,
				cert.NotAfter,
			)
		}
	}

	consenter.Identity = sID.IdBytes

	consenter.Comm = &cluster.AuthCommMgr{
		Logger:         flogging.MustGetLogger("orderer.common.bdls.New.AuthCommMgr.cluster"),
		Metrics:        clusterMetrics,
		SendBufferSize: conf.General.Cluster.SendBufferSize,
		Chan2Members:   make(cluster.MembersByChannel),
		Connections:    cluster.NewConnectionMgr(clusterDialer.Config),
		Signer:         signerSerializer,
		NodeIdentity:   sID.IdBytes,
	}

	//TODO: wierd doubt
	comm := createComm(clusterDialer, consenter, conf.General.Cluster, metricsProvider)
	consenter.Communication = comm
	consenter.ClusterService = &cluster.ClusterService{
		CertExpWarningThreshold:          conf.General.Cluster.CertExpirationWarningThreshold,
		MinimumExpirationWarningInterval: cluster.MinimumExpirationWarningInterval,
		StreamCountReporter: &cluster.StreamCountReporter{
			Metrics: comm.Metrics,
		},
		StepLogger:          flogging.MustGetLogger("orderer.common.bdls.New.clusterservice.cluster.step"),
		Logger:              flogging.MustGetLogger("orderer.common.bdls.New.clusterservice.cluster"),
		MembershipByChannel: make(map[string]*cluster.ChannelMembersConfig),
		NodeIdentity:        sID.IdBytes,
		RequestHandler:      consenter.Dispatcher,
	}
	ab.RegisterClusterNodeServiceServer(srv.Server(), consenter.ClusterService)
	logger.Debugf("*************Cluster service registered with gRPC server-smartBFTstyle*************")
	svc := &cluster.Service{
		CertExpWarningThreshold:          conf.General.Cluster.CertExpirationWarningThreshold,
		MinimumExpirationWarningInterval: cluster.MinimumExpirationWarningInterval,
		StreamCountReporter: &cluster.StreamCountReporter{
			Metrics: comm.Metrics,
		},
		StepLogger: flogging.MustGetLogger("orderer.common.cluster.step"),
		Logger:     flogging.MustGetLogger("orderer.common.cluster"),
		Dispatcher: comm,
	}
	ab.RegisterClusterServer(srv.Server(), svc)
	logger.Debugf("************Cluster service registered with gRPC server-raftstyle**************")

	return consenter
}

// HandleChain returns a new Chain instance or an error upon failure
func (c *Consenter) HandleChain(support consensus.ConsenterSupport, metadata *cb.Metadata) (consensus.Chain, error) {
	// TODO: Implement the logic to handle the chain creation
	// Initialize and return a new Chain instance
	consenters := support.SharedConfig().Consenters()

	selfID, err := c.detectSelfID(consenters)
	if err != nil {
		return nil, errors.Wrap(err, "without a system channel, a follower should have been created")
	}
	c.Logger.Infof("Local consenter id is %d", selfID)

	opts := Options{
		RPCTimeout: c.Conf.General.Cluster.RPCTimeout,
		BDLSid:     (uint64)(selfID),
		Clock:      clock.NewClock(),
		//MemoryStorage: raft.NewMemoryStorage(),
		Logger: c.Logger,

		//TickInterval:         tickInterval,
		//ElectionTick:         int(m.Options.ElectionTick),
		//HeartbeatTick:        int(m.Options.HeartbeatTick),
		// I have set MaxInflightBlocks to 1 for testing purposes
		MaxInflightBlocks: 1,
		// for now i have disabled it
		//MaxSizePerMsg:        uint64(support.SharedConfig().BatchSize().PreferredMaxBytes),
		//SnapshotIntervalSize: m.Options.SnapshotIntervalSize,

		//BlockMetadata: blockMetadata,
		Consenters: consenters,

		//MigrationInit: isMigration,

		//WALDir:            path.Join(c.EtcdRaftConfig.WALDir, support.ChannelID()),
		//SnapDir:           path.Join(c.EtcdRaftConfig.SnapDir, support.ChannelID()),
		//EvictionSuspicion: evictionSuspicion,
		TLSCert: c.TLSCert,
		Metrics: c.Metrics,
	}

	// Called after the etcdraft.Chain halts when it detects eviction from the cluster.
	// When we do NOT have a system channel, we switch to a follower.Chain upon eviction.
	c.Logger.Info("After eviction from the cluster Registrar.SwitchToFollower will be called, and the orderer will become a follower of the channel.")
	//haltCallback := func() { c.ChainManager.SwitchChainToFollower(support.ChannelID()) }

	rpc := &cluster.RPC{
		Timeout:       c.OrdererConfig.General.Cluster.RPCTimeout,
		Logger:        c.Logger,
		Channel:       support.ChannelID(),
		Comm:          c.Communication,
		StreamsByType: cluster.NewStreamsByType(),
	}

	logger := flogging.MustGetLogger("orderer.common.bdls.HandleChain")
	logger.Debugf("********************Okay calling new chain with selfID: %d****************************", selfID)
	return NewChain(
		// taken from smartBFT
		(uint64)(selfID),
		c.ClusterDialer,
		c.Conf.General.Cluster,
		c.Comm,
		c.SignerSerializer,
		c.GetPolicyManager(support.ChannelID()),
		c.Metrics,
		// take from etcdraft
		support,
		opts,
		c.Communication,
		rpc,
		c.BCCSP,
		func() (BlockPuller, error) {
			return NewBlockPuller(support, c.ClusterDialer, c.OrdererConfig.General.Cluster, c.BCCSP)
		},
	)

}

func createComm(clusterDialer *cluster.PredicateDialer, c *Consenter, config localconfig.Cluster, p metrics.Provider) *cluster.Comm {
	metrics := cluster.NewMetrics(p)
	logger := flogging.MustGetLogger("orderer.common.bdls.createcomm.cluster")
	logger.Debugf("--------------Creating cluster communication with config -----------------------")

	logger.Debugf("***************************Cluster config: ListenAddress=%s, ListenPort=%d, ServerCert=%s, ServerKey=%s, CertExpirationWarning=%v***************************************",
		config.ListenAddress,
		config.ListenPort,
		filepath.Base(config.ServerCertificate),
		filepath.Base(config.ServerPrivateKey),
		config.CertExpirationWarningThreshold,
	)

	compareCert := cluster.CachePublicKeyComparisons(func(a, b []byte) bool {
		err := crypto.CertificatesWithSamePublicKey(a, b)
		if err != nil && err != crypto.ErrPubKeyMismatch {
			crypto.LogNonPubKeyMismatchErr(logger.Errorf, err, a, b)
		}
		return err == nil
	})

	comm := &cluster.Comm{
		MinimumExpirationWarningInterval: cluster.MinimumExpirationWarningInterval,
		CertExpWarningThreshold:          config.CertExpirationWarningThreshold,
		SendBufferSize:                   config.SendBufferSize,
		Logger:                           logger,
		Chan2Members:                     make(map[string]cluster.MemberMapping),
		Connections:                      cluster.NewConnectionStore(clusterDialer, metrics.EgressTLSConnectionCount),
		Metrics:                          metrics,
		ChanExt:                          c,
		H:                                c.ClusterService.RequestHandler,
		CompareCertificate:               compareCert,
	}
	c.Communication = comm
	return comm
}

func (c *Consenter) detectSelfID(consenters []*cb.Consenter) (uint32, error) {
	logger := flogging.MustGetLogger("orderer.common.bdls.consenter.detectSelfID")
	logger.Debugf("--------------Detecting self ID in consenters-----------------------")

	for _, cst := range consenters {
		santizedCert, err := crypto.SanitizeX509Cert(cst.Identity)
		if err != nil {
			logger.Debugf("Failed to sanitize certificate for consenter %s: %v", cst.MspId, err)
			return 0, err
		}
		if bytes.Equal(c.Comm.NodeIdentity, santizedCert) {
			logger.Debugf("Found self ID %d in consenters which have MspID %s", cst.Id, cst.MspId)
			return cst.Id, nil
		}
	}
	c.Logger.Warning("Could not find the node in channel consenters set")
	return 0, cluster.ErrNotInChannel
}
