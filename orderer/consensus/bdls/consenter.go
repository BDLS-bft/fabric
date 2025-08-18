package bdls

import (
	"bytes"

	// "reflect"
	"time"

	"code.cloudfoundry.org/clock"
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
	//"github.com/hyperledger/fabric/common/policies"
	"github.com/hyperledger/fabric/internal/pkg/comm"
	"github.com/hyperledger/fabric/orderer/common/cluster"
	"github.com/hyperledger/fabric/orderer/common/localconfig"
	"github.com/hyperledger/fabric/orderer/consensus"

	//"github.com/hyperledger/fabric/orderer/consensus/bdls/util"
	//"github.com/hyperledger/fabric/orderer/consensus/bdls/wal"
	// "github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	//"go.uber.org/zap"
	//"google.golang.org/protobuf/proto"
)

// ChainManager defines the methods from multichannel.Registrar needed by the Consenter.
type ChainManager interface {
	GetConsensusChain(channelID string) consensus.Chain
	CreateChain(channelID string)
	SwitchChainToFollower(channelID string)
	//ReportConsensusRelationAndStatusMetrics(channelID string, relation types.ConsensusRelation, status types.Status)
}

//  no need of policy manager
// type PolicyManagerRetriever func(channelID string) policies.Manager

// Minimum BDLS-Fabric Config for prototype
type Config struct {
	TickInterval time.Duration
}

// Consenter implementation of the BFT smart based consenter
type Consenter struct {
	Logger        *flogging.FabricLogger
	Comm          *cluster.AuthCommMgr
	ChainManager  ChainManager
	ClusterDialer *cluster.PredicateDialer
	Metrics       *Metrics
	BCCSP         bccsp.BCCSP
	Communication cluster.Communicator
	TLSCert       []byte
	TLSPrivKey    []byte
	*Dispatcher
	OrdererConfig localconfig.TopLevel
	BDLSConfig    Config
	srvConf       comm.ServerConfig
	//Registrar        *multichannel.Registrar
	//ClusterService *cluster.ClusterService
	//Identity         []byte
	//WALBaseDir       string
	//GetPolicyManager PolicyManagerRetriever
	//Conf             *localconfig.TopLevel
	//SignerSerializer SignerSerializer
	//MetricsBFT       *api.Metrics
	//MetricsWalBFT    *wal.Metrics
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
	clusterDialer *cluster.PredicateDialer,
	conf *localconfig.TopLevel,
	srvConf comm.ServerConfig, // TODO why is this not used?
	srv *comm.GRPCServer,
	registrar ChainManager,
	metricsProvider metrics.Provider,
	BCCSP bccsp.BCCSP,
	// pmr PolicyManagerRetriever,
	// signerSerializer SignerSerializer,
	clusterMetrics *cluster.Metrics,
	// r *multichannel.Registrar,
) *Consenter {
	logger := flogging.MustGetLogger("orderer.consensus.bdls.New")

	logger.Debugf("*************Creating a new BDLS Consenter****************************")

	cfg := Config{
		TickInterval: 20 * time.Millisecond, // TickInterval is the time between two consecutive ticks
	}

	consenter := &Consenter{
		ChainManager:  registrar,
		ClusterDialer: clusterDialer,
		Logger:        logger,
		Metrics:       NewMetrics(metricsProvider),
		BCCSP:         BCCSP,
		TLSCert:       srvConf.SecOpts.Certificate,
		OrdererConfig: *conf,
		BDLSConfig:    cfg,
		TLSPrivKey:    srvConf.SecOpts.Key,
		// CreateChain: r.CreateChain,
		// Conf:             conf,
		//WALBaseDir:       walConfig.WALDir,
		// MetricsBFT:       api.NewMetrics(mpc, "channel"),
		// MetricsWalBFT:    wal.NewMetrics(mpc, "channel"),
		// Registrar:        r,
		// GetPolicyManager: pmr,
		// Chains:           r,
		// SignerSerializer: signerSerializer,
		// CreateChain: r.CreateChain,
	}

	logger.Debugf("TLS Cert PEM:\n%s", srvConf.SecOpts.Certificate)
	logger.Debugf("TLS Key PEM:\n%s", srvConf.SecOpts.Key)

	consenter.Dispatcher = &Dispatcher{
		Logger:        logger,
		ChainSelector: consenter,
	}

	// identity, _ := signerSerializer.Serialize()
	// sID := &msp.SerializedIdentity{}
	// if err := proto.Unmarshal(identity, sID); err != nil {
	// 	logger.Panicf("failed unmarshaling identity: %s", err)
	// }

	// block, _ := pem.Decode(sID.IdBytes)
	// if block == nil {
	// 	logger.Warningf("Failed to decode identity certificate PEM for MSP: %s", sID.Mspid)
	// } else {
	// 	cert, err := x509.ParseCertificate(block.Bytes)
	// 	if err != nil {
	// 		logger.Warningf("Failed to parse identity certificate for MSP %s: %v", sID.Mspid, err)
	// 	} else {
	// 		// Log the structured, human-readable identity information
	// 		logger.Infof(
	// 			"Loaded Consenter Identity | MSP: %s, Subject: %s, Expires: %v",
	// 			sID.Mspid,
	// 			cert.Subject,
	// 			cert.NotAfter,
	// 		)
	// 	}
	// }

	// consenter.Identity = sID.IdBytes

	// consenter.Comm = &cluster.AuthCommMgr{
	// 	Logger:         flogging.MustGetLogger("orderer.common.bdls.New.AuthCommMgr.cluster"),
	// 	Metrics:        clusterMetrics,
	// 	SendBufferSize: conf.General.Cluster.SendBufferSize,
	// 	Chan2Members:   make(cluster.MembersByChannel),
	// 	Connections:    cluster.NewConnectionMgr(clusterDialer.Config),
	// 	Signer:         signerSerializer,
	// 	NodeIdentity:   sID.IdBytes,
	// }

	// //TODO: wierd doubt
	// comm := createComm(clusterDialer, consenter, conf.General.Cluster, metricsProvider)
	// consenter.Communication = comm
	// consenter.ClusterService = &cluster.ClusterService{
	// 	CertExpWarningThreshold:          conf.General.Cluster.CertExpirationWarningThreshold,
	// 	MinimumExpirationWarningInterval: cluster.MinimumExpirationWarningInterval,
	// 	StreamCountReporter: &cluster.StreamCountReporter{
	// 		Metrics: comm.Metrics,
	// 	},
	// 	StepLogger:          flogging.MustGetLogger("orderer.common.bdls.New.clusterservice.cluster.step"),
	// 	Logger:              flogging.MustGetLogger("orderer.common.bdls.New.clusterservice.cluster"),
	// 	MembershipByChannel: make(map[string]*cluster.ChannelMembersConfig),
	// 	NodeIdentity:        sID.IdBytes,
	// 	RequestHandler:      consenter.Dispatcher,
	// }
	// ab.RegisterClusterNodeServiceServer(srv.Server(), consenter.ClusterService)
	// logger.Debugf("*************Cluster service registered with gRPC server-smartBFTstyle*************")
	// svc := &cluster.Service{
	// 	CertExpWarningThreshold:          conf.General.Cluster.CertExpirationWarningThreshold,
	// 	MinimumExpirationWarningInterval: cluster.MinimumExpirationWarningInterval,
	// 	StreamCountReporter: &cluster.StreamCountReporter{
	// 		Metrics: comm.Metrics,
	// 	},
	// 	StepLogger: flogging.MustGetLogger("orderer.common.cluster.step"),
	// 	Logger:     flogging.MustGetLogger("orderer.common.cluster"),
	// 	Dispatcher: comm,
	// }
	// ab.RegisterClusterServer(srv.Server(), svc)
	// logger.Debugf("************Cluster service registered with gRPC server-raftstyle**************")

	comm := createComm(clusterDialer, consenter, conf.General.Cluster, metricsProvider)
	consenter.Communication = comm
	logger.Debugf("*************Cluster communication created successfully****************************")
	svc := &cluster.Service{
		CertExpWarningThreshold:          conf.General.Cluster.CertExpirationWarningThreshold,
		MinimumExpirationWarningInterval: cluster.MinimumExpirationWarningInterval,
		StreamCountReporter: &cluster.StreamCountReporter{
			Metrics: comm.Metrics,
		},
		StepLogger: flogging.MustGetLogger("orderer.common.bdls.cluster.step"),
		Logger:     flogging.MustGetLogger("orderer.common.bdls.cluster"),
		Dispatcher: comm,
	}
	logger.Infof("Registering Cluster service with gRPC server")
	ab.RegisterClusterServer(srv.Server(), svc)

	logger.Debugf("************Cluster service registered with gRPC server-bdls style**************")

	return consenter
}

// HandleChain returns a new Chain instance or an error upon failure
func (c *Consenter) HandleChain(support consensus.ConsenterSupport, metadata *cb.Metadata) (consensus.Chain, error) {

	logger := flogging.MustGetLogger("orderer.common.bdls.HandleChain")
	logger.Infof("*********************Handle Chain Called****************************")
	consenters := support.SharedConfig().Consenters()

	selfID, err := c.detectSelfID(consenters)
	if err != nil {
		return nil, errors.Wrap(err, "without a system channel, a follower should have been created")
	}
	c.Logger.Infof("Local consenter id is %d", selfID)

	opts := Options{
		RPCTimeout:        c.OrdererConfig.General.Cluster.RPCTimeout,
		BDLSid:            (uint64)(selfID),
		Clock:             clock.NewClock(),
		TickInterval:      20 * time.Millisecond, // Default tick interval
		Logger:            c.Logger,
		MaxInflightBlocks: 1,
		Consenters:        consenters,
		TLSCert:           c.TLSCert,
		TLSPrivKey:        c.TLSPrivKey,
		Metrics:           c.Metrics,
		MaxSizePerMsg:     uint64(support.SharedConfig().BatchSize().PreferredMaxBytes),
		srvConf:           c.srvConf,
		conf:              &c.OrdererConfig,
		//MemoryStorage: raft.NewMemoryStorage(),
		//ElectionTick:         int(m.Options.ElectionTick),
		//HeartbeatTick:        int(m.Options.HeartbeatTick),
		// I have set MaxInflightBlocks to 1 for testing purposes
		// for now i have disabled it
		//MaxSizePerMsg:        uint64(support.SharedConfig().BatchSize().PreferredMaxBytes),
		//SnapshotIntervalSize: m.Options.SnapshotIntervalSize,
		//BlockMetadata: blockMetadata,
		//MigrationInit: isMigration,
		//WALDir:            path.Join(c.EtcdRaftConfig.WALDir, support.ChannelID()),
		//SnapDir:           path.Join(c.EtcdRaftConfig.SnapDir, support.ChannelID()),
		//EvictionSuspicion: evictionSuspicion,
	}

	rpc := &cluster.RPC{
		Timeout:       c.OrdererConfig.General.Cluster.RPCTimeout,
		Logger:        c.Logger,
		Channel:       support.ChannelID(),
		Comm:          c.Communication,
		StreamsByType: cluster.NewStreamsByType(),
	}

	logger.Debugf("********************Okay calling new chain with selfID: %d****************************", selfID)
	return NewChain(
		c,
		uint64(selfID),
		support,
		opts,
		c.Communication,
		rpc,
		c.BCCSP,
		func() (BlockPuller, error) {
			return NewBlockPuller(support, c.ClusterDialer, c.OrdererConfig.General.Cluster, c.BCCSP)
		},
		c.Metrics,
	)

}

func createComm(clusterDialer *cluster.PredicateDialer, c *Consenter, config localconfig.Cluster, p metrics.Provider) *cluster.Comm {
	metrics := cluster.NewMetrics(p)
	logger := flogging.MustGetLogger("orderer.common.bdls.createcomm.cluster")
	logger.Debugf("--------------Creating cluster communication with config -----------------------")

	// logger.Debugf("***************************Cluster config: ListenAddress=%s, ListenPort=%d, ServerCert=%s, ServerKey=%s, CertExpirationWarning=%v***************************************",
	// 	config.ListenAddress,
	// 	config.ListenPort,
	// 	filepath.Base(config.ServerCertificate),
	// 	filepath.Base(config.ServerPrivateKey),
	// 	config.CertExpirationWarningThreshold,
	// )

	compareCert := cluster.CachePublicKeyComparisons(func(a, b []byte) bool {
		err := crypto.CertificatesWithSamePublicKey(a, b)
		if err != nil && err != crypto.ErrPubKeyMismatch {
			logger.Debugf("Failed to compare certificates: %v", err)
			crypto.LogNonPubKeyMismatchErr(logger.Errorf, err, a, b)
		}
		return err == nil
	})

	logger.Debugf("***********compare cert done****************************")

	comm := &cluster.Comm{
		MinimumExpirationWarningInterval: cluster.MinimumExpirationWarningInterval,
		CertExpWarningThreshold:          config.CertExpirationWarningThreshold,
		SendBufferSize:                   config.SendBufferSize,
		Logger:                           logger,
		Chan2Members:                     make(map[string]cluster.MemberMapping),
		Connections:                      cluster.NewConnectionStore(clusterDialer, metrics.EgressTLSConnectionCount),
		Metrics:                          metrics,
		ChanExt:                          c,
		H:                                c,
		CompareCertificate:               compareCert,
	}

	logger.Debugf("***********cluster communication created****************************")
	// c.Communication = comm
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
