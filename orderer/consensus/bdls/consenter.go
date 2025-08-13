package bdls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"fmt"
	"time"

	bdlslib "github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric-lib-go/bccsp"
	bccsputils "github.com/hyperledger/fabric-lib-go/bccsp/utils"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-lib-go/common/metrics"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	msp "github.com/hyperledger/fabric-protos-go-apiv2/msp"
	ab "github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric/common/channelconfig"
	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/internal/pkg/comm"
	"github.com/hyperledger/fabric/internal/pkg/identity"
	"github.com/hyperledger/fabric/orderer/common/cluster"
	"github.com/hyperledger/fabric/orderer/common/localconfig"
	"github.com/hyperledger/fabric/orderer/common/multichannel"
	"github.com/hyperledger/fabric/orderer/consensus"
	"github.com/hyperledger/fabric/protoutil"
	"google.golang.org/protobuf/proto"
)

// ChainGetter obtains per-channel support from registrar
type ChainGetter interface {
    GetConsensusChain(channelID string) consensus.Chain
}

// Implement consensus.ClusterConsenter so the registrar treats BDLS as a cluster consenter
func (c *Consenter) IsChannelMember(joinBlock *cb.Block) (bool, error) {
    if joinBlock == nil {
        return false, fmt.Errorf("nil block")
    }
    env, err := protoutil.ExtractEnvelope(joinBlock, 0)
    if err != nil {
        return false, err
    }
    bundle, err := channelconfig.NewBundleFromEnvelope(env, c.BCCSP)
    if err != nil {
        return false, err
    }
    oc, exists := bundle.OrdererConfig()
    if !exists {
        return false, fmt.Errorf("no orderer config in bundle")
    }
    // Compare sanitized local identity with consenters' identities
    sanitizedLocal, err := crypto.SanitizeX509Cert(c.Identity)
    if err != nil {
        return false, err
    }
    for _, consenter := range oc.Consenters() {
        santizedCert, err := crypto.SanitizeX509Cert(consenter.Identity)
        if err != nil {
            return false, err
        }
        if bytes.Equal(sanitizedLocal, santizedCert) {
            return true, nil
        }
    }
    return false, nil
}
type Consenter struct {
    Chains         ChainGetter
    Logger         *flogging.FabricLogger
    Registrar      *multichannel.Registrar
    ClusterDialer  *cluster.PredicateDialer
    Comm           *cluster.AuthCommMgr
    ClusterService *cluster.ClusterService
    Conf           *localconfig.TopLevel
    BCCSP          bccsp.BCCSP
    Identity       []byte
}

func New(
    signer identity.SignerSerializer,
    clusterDialer *cluster.PredicateDialer,
    conf *localconfig.TopLevel,
    srvConf comm.ServerConfig,
    srv *comm.GRPCServer,
    r *multichannel.Registrar,
    metricsProvider metrics.Provider,
    BCCSP bccsp.BCCSP,
) *Consenter {
    logger := flogging.MustGetLogger("orderer.consensus.bdls")

    identityBytes, _ := signer.Serialize()
    // Extract raw cert bytes from serialized identity
    var sID msp.SerializedIdentity
    _ = proto.Unmarshal(identityBytes, &sID)
    nodeIdentity := sID.IdBytes
    // Sanitize local identity to match ConfigureNodeCerts sanitization
    sanitizedLocal, _ := crypto.SanitizeX509Cert(nodeIdentity)

    consenter := &Consenter{
        Chains:        r,
        Logger:        logger,
        Registrar:     r,
        ClusterDialer: clusterDialer,
        Conf:          conf,
        BCCSP:         BCCSP,
        Identity:      nodeIdentity,
    }

    consenter.Comm = &cluster.AuthCommMgr{
        Logger:         flogging.MustGetLogger("orderer.common.cluster"),
        Metrics:        cluster.NewMetrics(metricsProvider),
        SendBufferSize: conf.General.Cluster.SendBufferSize,
        Chan2Members:   make(cluster.MembersByChannel),
        Connections:    cluster.NewConnectionMgr(clusterDialer.Config),
        Signer:         signer,
        NodeIdentity:   nodeIdentity,
    }

    consenter.ClusterService = &cluster.ClusterService{
        StreamCountReporter: &cluster.StreamCountReporter{Metrics: consenter.Comm.Metrics},
        Logger:              flogging.MustGetLogger("orderer.common.cluster"),
        StepLogger:          flogging.MustGetLogger("orderer.common.cluster.step"),
        MinimumExpirationWarningInterval: cluster.MinimumExpirationWarningInterval,
        CertExpWarningThreshold:          conf.General.Cluster.CertExpirationWarningThreshold,
        MembershipByChannel:              make(map[string]*cluster.ChannelMembersConfig),
        NodeIdentity:                     sanitizedLocal,
        RequestHandler: &Ingress{
            Logger:        logger,
            ChainSelector: consenter,
        },
    }

    ab.RegisterClusterNodeServiceServer(srv.Server(), consenter.ClusterService)

    return consenter
}

func (c *Consenter) HandleChain(support consensus.ConsenterSupport, metadata *cb.Metadata) (consensus.Chain, error) {
    // Configure cluster membership for this channel
    consenters := support.SharedConfig().Consenters()
    _ = c.ClusterService.ConfigureNodeCerts(support.ChannelID(), consenters)

    // Build BDLS participants from consenter public keys (from cert identities)
    var participants []bdlslib.Identity
    for _, cn := range consenters {
        // Identity may be PEM or DER. Decode PEM if needed.
        idBytes := cn.Identity
        if p, _ := pemDecodeIfPEM(idBytes); p != nil {
            idBytes = p
        }
        cert, err := x509.ParseCertificate(idBytes)
        if err != nil {
            continue
        }
        if pk, ok := cert.PublicKey.(*ecdsa.PublicKey); ok {
            participants = append(participants, bdlslib.DefaultPubKeyToIdentity(pk))
        }
    }

    // Extract local public key
    var localPub *ecdsa.PublicKey
    der := c.Identity
    if p, _ := pemDecodeIfPEM(der); p != nil {
        der = p
    }
    if cert, err := x509.ParseCertificate(der); err == nil {
        if pk, ok := cert.PublicKey.(*ecdsa.PublicKey); ok {
            localPub = pk
        }
    }

    // SignDigest via Fabric signer -> DER => (r,s)
    signDigest := func(digest []byte) ([]byte, []byte, error) {
        sigDER, err := c.Comm.Signer.Sign(digest)
        if err != nil {
            return nil, nil, err
        }
        r, s, err := bccsputils.UnmarshalECDSASignature(sigDER)
        if err != nil {
            return nil, nil, err
        }
        return trimLeadingZeros(r.Bytes()), trimLeadingZeros(s.Bytes()), nil
    }

    cfg := &bdlslib.Config{
        Epoch:               time.Now(),
        CurrentHeight:       support.Height(),
        SignDigest:          signDigest,
        PublicKey:           localPub,
        Participants:        participants,
        EnableCommitUnicast: false,
        StateCompare: func(a bdlslib.State, b bdlslib.State) int { return bytes.Compare(a, b) },
        StateValidate: func(bdlslib.State) bool { return true },
        PubKeyToIdentity:    bdlslib.DefaultPubKeyToIdentity,
        MessageValidator: func(_ *bdlslib.Consensus, _ *bdlslib.Message, _ *bdlslib.SignedProto) bool {
            return true
        },
        MessageOutCallback: func(m *bdlslib.Message, sp *bdlslib.SignedProto) { _ = m; _ = sp },
    }

    cons, err := bdlslib.NewConsensus(cfg)
    if err != nil {
        return nil, err
    }
    // Map BatchTimeout to BDLS latency baseline (simple mapping for now)
    if support.SharedConfig() != nil {
        cons.SetLatency(support.SharedConfig().BatchTimeout())
    } else {
        cons.SetLatency(bdlslib.DefaultConsensusLatency)
    }

    ch := newChain()
    ch.support = support
    ch.Channel = support.ChannelID()
    ch.consensus = cons
    ch.bc = support.BlockCutter()

    // Configure communicator membership from consenters with endpoint and TLS material
    var nodes []cluster.RemoteNode
    for _, cs := range consenters {
        endpoint := fmt.Sprintf("%s:%d", cs.Host, cs.Port)

        // Convert PEM to DER for TLS certs if needed
        serverCertDER, err := pemToDER(cs.ServerTlsCert)
        if err != nil {
            // If not PEM, assume already DER
            serverCertDER = cs.ServerTlsCert
        }
        clientCertDER, err := pemToDER(cs.ClientTlsCert)
        if err != nil {
            clientCertDER = cs.ClientTlsCert
        }

        // Prefer dialer-configured ServerRootCAs; fall back to empty slice
        var rootCAs [][]byte
        if len(c.ClusterDialer.Config.SecOpts.ServerRootCAs) > 0 {
            rootCAs = c.ClusterDialer.Config.SecOpts.ServerRootCAs
        } else {
            // As a dev fallback, trust the remote server certificate directly
            rootCAs = [][]byte{serverCertDER}
        }

        nodes = append(nodes, cluster.RemoteNode{
            NodeAddress: cluster.NodeAddress{
                ID:       uint64(cs.Id),
                Endpoint: endpoint,
            },
            NodeCerts: cluster.NodeCerts{
                ServerTLSCert: serverCertDER,
                ClientTLSCert: clientCertDER,
                ServerRootCA:  rootCAs,
                Identity:      cs.Identity,
            },
        })
    }
    c.Comm.Configure(support.ChannelID(), nodes)

    // Create per-peer RPC and join peers into BDLS for egress
    rpc := &cluster.RPC{
        Logger:        flogging.MustGetLogger("orderer.consensus.bdls.rpc"),
        Timeout:       5 * time.Minute,
        Channel:       support.ChannelID(),
        Comm:          c.Comm,
        StreamsByType: cluster.NewStreamsByType(),
    }
    for _, cs := range consenters {
        var pub *ecdsa.PublicKey
        // Identity may be PEM or DER. Decode PEM if needed.
        idBytes := cs.Identity
        if p, _ := pemDecodeIfPEM(idBytes); p != nil {
            idBytes = p
        }
        if cert, err := x509.ParseCertificate(idBytes); err == nil {
            if pk, ok := cert.PublicKey.(*ecdsa.PublicKey); ok {
                pub = pk
            }
        }
        peer := &fabricPeer{id: uint64(cs.Id), rpc: rpc, pub: pub}
        cons.Join(peer)
    }
    // Initialize a per-chain logger and emit an informational startup message
    if ch.Logger == nil {
        ch.Logger = flogging.MustGetLogger("orderer.consensus.bdls.chain").With("channel", ch.Channel)
    }
    ch.Logger.Infof("Starting BDLS node channel=%s", ch.Channel)
    return ch, nil
}

// ReceiverByChain returns the BDLS Chain for the given channelID or nil if not found.
func (c *Consenter) ReceiverByChain(channelID string) *Chain {
    if c == nil || c.Chains == nil {
        return nil
    }
    chain := c.Chains.GetConsensusChain(channelID)
    if chain == nil {
        return nil
    }
    if bdlsChain, ok := chain.(*Chain); ok {
        return bdlsChain
    }
    return nil
}

// TargetChannel extracts the channel from the given proto.Message. Empty string on failure.
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
