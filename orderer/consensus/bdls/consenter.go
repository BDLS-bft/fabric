/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/pem"
	"reflect"
	"time"

	bdlslib "github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric-lib-go/bccsp"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-lib-go/common/metrics"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"github.com/hyperledger/fabric/common/channelconfig"
	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/internal/pkg/comm"
	"github.com/hyperledger/fabric/internal/pkg/identity"
	"github.com/hyperledger/fabric/orderer/common/cluster"
	"github.com/hyperledger/fabric/orderer/common/localconfig"
	"github.com/hyperledger/fabric/orderer/common/multichannel"
	"github.com/hyperledger/fabric/orderer/consensus"
	bdlsproto "github.com/hyperledger/fabric/orderer/consensus/bdls/protos"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// Consenter wires the BDLS chain into Fabric's orderer. The shape of New()
// matches smartbft.New() with two extra arguments (cluster.Communicator,
// *cluster.ClusterService) that main.go fills in by sharing smartbft's
// instances — gRPC only permits one ClusterNodeServiceServer registration
// per server, and a single AuthCommMgr keeps connection state in one place.
// See orderer/common/server/main.go for the multiplex handler that fans
// inbound StepRequests out to whichever consenter (smartbft or bdls) owns
// the target channel.
// ---------------------------------------------------------------------------

// ErrClusterTLSKeyUnavailable is returned by HandleChain when the BDLS
// consenter was instantiated without a parseable cluster TLS private
// key. The consenter still loads (so IsChannelMember works for
// cluster-join detection on non-BDLS channels) but any attempt to run
// a BDLS channel fails fast with this error rather than crashing deep
// inside bdls.NewConsensus.
var ErrClusterTLSKeyUnavailable = errors.New("bdls consenter: cluster TLS private key was not loaded at startup; set General.Cluster.ClientPrivateKey and restart the orderer")

// Consenter is the BDLS implementation of consensus.Consenter. Fields mirror
// smartbft.Consenter where they make sense so operators and reviewers who
// already know the BFT consenter don't have to relearn a new shape.
type Consenter struct {
	Logger           *flogging.FabricLogger
	Metrics          *Metrics
	BCCSP            bccsp.BCCSP
	SignerSerializer identity.SignerSerializer
	Identity         []byte

	// TLSPrivateKey is the orderer's cluster TLS private key, loaded
	// from conf.General.Cluster.ClientPrivateKey at New() time. Its
	// public half is what BDLS uses as our participant identity. See
	// signer.go for the full "why this key, not the MSP identity key"
	// rationale.
	TLSPrivateKey *ecdsa.PrivateKey
	// TLSPublicKey is derived from TLSPrivateKey and cached so the
	// hot-path detectSelfID loop does not repeatedly reach through the
	// pointer chain.
	TLSPublicKey *ecdsa.PublicKey

	// Conf is the top-level localconfig, used for Cluster timing knobs
	// when we construct the BlockPuller.
	Conf *localconfig.TopLevel
	// ClusterDialer is the shared cluster dialer (inherited from
	// etcdraft's initialisation — one dialer per orderer).
	ClusterDialer *cluster.PredicateDialer
	// Registrar is used by ReceiverByChain to look up per-channel chains.
	Registrar *multichannel.Registrar

	// Comm is the cluster.Communicator BDLS uses to send outbound
	// StepRequests. It is shared with smartbft: main.go constructs one
	// AuthCommMgr inside smartbft.New, then hands the same instance to
	// bdls.New so both consenters share a single connection pool and
	// single dial-in credentials. Per-channel fan-out happens via
	// cluster.RPC (one RPC per channel per consenter) wrapping this
	// shared Comm. BDLS HandleChain calls Comm.Configure(channel, nodes)
	// to register the channel's participant set.
	Comm cluster.Communicator
	// ClusterService is the shared cluster.ClusterService registered on
	// the gRPC server by smartbft.New (only one
	// ClusterNodeServiceServer can be registered per grpc.Server, so we
	// piggyback on smartbft's instead of creating a second one). BDLS
	// HandleChain calls ClusterService.ConfigureNodeCerts(channel,
	// consenters) to register which remote identities are authorized to
	// send StepRequests on each BDLS channel. main.go also replaces
	// ClusterService.RequestHandler with a multiplex handler that
	// dispatches to whichever of smartbft/bdls owns the target channel.
	ClusterService *cluster.ClusterService
}

// New constructs a BDLS consenter. Shape matches smartbft.New so
// orderer/common/server/main.go can slot it in next to the BFT
// consenter with one additional line, plus two extra arguments
// (comm, clusterSvc) for the shared cluster transport.
//
// The srvConf / srv arguments are accepted for signature compatibility
// but not used directly — BDLS piggybacks on the ClusterNodeService
// that smartbft registers on srv. Sharing one ClusterNodeServiceServer
// between consenters is not optional: gRPC only permits a single
// registration per service per server. See cluster_wiring.go and the
// multiplex handler installed in main.go for how inbound StepRequests
// are routed to the right consenter.
//
// Cluster TLS private-key loading happens here rather than inside
// HandleChain so that a misconfigured key is surfaced at orderer
// startup (one log line) rather than once per channel at join time
// (one log line per channel). If the key fails to load, the consenter
// still instantiates — IsChannelMember only needs the MSP identity,
// not the TLS private key — but HandleChain will refuse to run any
// BDLS channel until the operator fixes the configuration. This is a
// deliberate choice: we do not want a typo in General.Cluster.
// ClientPrivateKey to crash an orderer that is also serving
// etcdraft/BFT channels.
func New(
	signerSerializer identity.SignerSerializer,
	clusterDialer *cluster.PredicateDialer,
	conf *localconfig.TopLevel,
	_ comm.ServerConfig,
	_ *comm.GRPCServer,
	r *multichannel.Registrar,
	metricsProvider metrics.Provider,
	_ *cluster.Metrics,
	csp bccsp.BCCSP,
	clusterComm cluster.Communicator,
	clusterSvc *cluster.ClusterService,
) *Consenter {
	logger := flogging.MustGetLogger("orderer.consensus.bdls")

	c := &Consenter{
		Logger:           logger,
		Metrics:          NewMetrics(metricsProvider),
		BCCSP:            csp,
		SignerSerializer: signerSerializer,
		Conf:             conf,
		ClusterDialer:    clusterDialer,
		Registrar:        r,
		Comm:             clusterComm,
		ClusterService:   clusterSvc,
	}
	if signerSerializer != nil {
		idBytes, err := signerSerializer.Serialize()
		if err != nil {
			logger.Warnf("BDLS consenter: failed to serialise signer identity: %v", err)
		} else {
			sID := &msp.SerializedIdentity{}
			if err := proto.Unmarshal(idBytes, sID); err != nil {
				logger.Warnf("BDLS consenter: failed to unmarshal identity: %v", err)
			} else {
				c.Identity = sID.IdBytes
			}
		}
	}

	// Load the cluster TLS private key. Failure is logged but not
	// fatal; HandleChain surfaces ErrClusterTLSKeyUnavailable when a
	// BDLS channel actually tries to start.
	if conf != nil {
		keyPath := conf.General.Cluster.ClientPrivateKey
		if priv, err := loadClusterTLSPrivateKey(keyPath); err != nil {
			logger.Warnf("BDLS consenter: cluster TLS private key unavailable — BDLS channels will refuse to start until this is fixed: %v", err)
		} else {
			c.TLSPrivateKey = priv
			c.TLSPublicKey = &priv.PublicKey
			logger.Infof("BDLS consenter: loaded cluster TLS private key from %s", keyPath)
		}
	}
	return c
}

// HandleChain is called by the Registrar when a channel using
// ConsensusType=BDLS is (re)initialised. It parses the channel's BDLS
// ConfigMetadata, detects our consenter id, builds a fully-populated
// bdls.Config with SignDigest + PublicKey wired to the cluster TLS
// keypair, configures the shared cluster transport (Comm +
// ClusterService) for this channel, and returns a live *Chain ready
// for Start().
func (c *Consenter) HandleChain(support consensus.ConsenterSupport, metadata *cb.Metadata) (consensus.Chain, error) {
	_ = metadata // decide-proof catch-up from committed metadata is a follow-up (see chain.go run loop)

	if c.TLSPrivateKey == nil || c.TLSPublicKey == nil {
		return nil, ErrClusterTLSKeyUnavailable
	}
	if c.Comm == nil || c.ClusterService == nil {
		return nil, errors.New("bdls consenter: cluster Comm/ClusterService not wired; main.go must pass smartbft's shared instances into bdls.New")
	}

	md, err := parseConfigMetadata(support.SharedConfig().ConsensusMetadata())
	if err != nil {
		return nil, errors.Wrap(err, "parsing BDLS ConfigMetadata")
	}

	selfID, err := c.detectSelfID(md)
	if err != nil {
		return nil, errors.Wrap(err, "detecting BDLS self id")
	}
	c.Logger.Infof("BDLS HandleChain: channel=%s selfID=%d consenters=%d", support.ChannelID(), selfID, len(md.Consenters))

	// Build the bdls.Config: Δ knobs, participants, StateCompare /
	// StateValidate, ReliableDecide default. A malformed Options block
	// (e.g. negative Δ, too few consenters) is surfaced as a
	// HandleChain error rather than silently tolerated.
	cfg, err := buildBDLSConfig(md, support.Height())
	if err != nil {
		return nil, errors.Wrap(err, "building bdls.Config")
	}

	// Wire the private key. The BDLS library's Config.PrivateKey is
	// the single field that drives both signing (BDLS calls
	// ecdsa.Sign internally on each <roundchange>/<lock>/<commit>) and
	// identity derivation (DefaultPubKeyToIdentity(&PrivateKey.PublicKey)
	// is used to find this node in the Participants slice).
	cfg.PrivateKey = c.TLSPrivateKey

	// Resolve the channel's cluster-layer membership (TLS certs, MSP
	// TLS root CAs, endpoints) by walking the last config block. This
	// mirrors smartbft's remoteNodesFromConfigBlock so two cluster
	// consenters on the same orderer never disagree about membership.
	remoteNodes, channelConsenters, err := buildRemoteNodes(support, c.BCCSP, c.Logger)
	if err != nil {
		return nil, errors.Wrap(err, "building cluster remote-node view")
	}
	if selfID >= uint64(len(channelConsenters)) {
		return nil, errors.Errorf("detected BDLS self index %d outside channel consenter set of size %d", selfID, len(channelConsenters))
	}
	selfConsenterID := channelConsenters[selfID].Id

	// Configure the *inbound* path: teach the shared ClusterService
	// which identities are authorized to send StepRequests on this
	// channel. Without this call, the multiplex handler would reach
	// bdls.Dispatcher but the auth layer would reject the sender as
	// "unknown".
	if err := c.ClusterService.ConfigureNodeCerts(support.ChannelID(), channelConsenters); err != nil {
		return nil, errors.Wrap(err, "configuring cluster service node certs")
	}

	// Configure the *outbound* path: register the channel's remote
	// nodes with the shared AuthCommMgr so cluster.RPC.SendConsensus
	// has a place to dial. Comm.Configure is idempotent on the
	// (channel, members) pair — calling it again on chain rebuild
	// replaces the previous member set cleanly.
	c.Comm.Configure(support.ChannelID(), remoteNodes)

	// Per-channel RPC wrapping the shared Comm. The 5-minute timeout
	// matches smartbft's egress — long enough to tolerate a transient
	// dial hiccup while smartbft/cluster reconnects, short enough that
	// a genuinely dead peer does not pin a goroutine forever.
	chanRPC := &cluster.RPC{
		Logger:        flogging.MustGetLogger("orderer.consensus.bdls.rpc").With("channel", support.ChannelID()),
		Channel:       support.ChannelID(),
		StreamsByType: cluster.NewStreamsByType(),
		Comm:          c.Comm,
		Timeout:       5 * time.Minute,
	}

	// One peerAdapter per remote (non-self) consenter. The destination
	// id is the channel-config consenter id (same one the
	// ClusterService/AuthCommMgr use for routing), NOT the index into
	// md.Consenters. We align the two slices by position since
	// buildRemoteNodes returns channelConsenters in the same order as
	// oc.Consenters(), and md.Consenters is populated from the same
	// source at configtxgen time.
	peers := make([]*peerAdapter, 0, len(channelConsenters)-1)
	for i, co := range channelConsenters {
		if uint64(i) == selfID {
			continue
		}
		pub, err := publicKeyFromTLSCert(co.ServerTlsCert)
		if err != nil {
			return nil, errors.Wrapf(err, "extracting public key for consenter id=%d", co.Id)
		}
		pa, err := newPeerAdapter(
			flogging.MustGetLogger("orderer.consensus.bdls.peer").With("channel", support.ChannelID()),
			chanRPC,
			support.ChannelID(),
			uint64(co.Id),
			pub,
			co.Host,
			co.Port,
		)
		if err != nil {
			return nil, errors.Wrapf(err, "constructing peer adapter for consenter id=%d", co.Id)
		}
		peers = append(peers, pa)
	}

	// Construct the BDLS state machine. bdlslib.NewConsensus validates
	// the Config internally (rejects too few participants, nil
	// SignDigest, etc.), so any configuration bug we missed upstream
	// shows up here with a descriptive error.
	bdlsConsensus, err := bdlslib.NewConsensus(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "bdlslib.NewConsensus")
	}
	// Join each peer into the consensus instance so BDLS can route
	// outbound <propose>/<lock>/<decide> messages through our adapters.
	for _, p := range peers {
		bdlsConsensus.Join(p)
	}

	chain, err := NewChain(support, bdlsConsensus, md, peers, c.Metrics, selfConsenterID)
	if err != nil {
		return nil, errors.Wrap(err, "constructing BDLS chain")
	}
	c.Logger.Infof("BDLS HandleChain: channel=%s constructed live chain (peers=%d, selfID=%d, consenterID=%d)",
		support.ChannelID(), len(peers), selfID, selfConsenterID)
	return chain, nil
}

// ReceiverByChain implements the dispatcher.ReceiverGetter interface. It
// looks up the per-channel BDLS chain via the Registrar and returns nil
// for channels we do not (yet) own.
func (c *Consenter) ReceiverByChain(channelID string) MessageReceiver {
	cs := c.Registrar.GetChain(channelID)
	if cs == nil {
		return nil
	}
	if cs.Chain == nil {
		c.Logger.Warnf("BDLS ReceiverByChain: channel %s has nil Chain; ignoring", channelID)
		return nil
	}
	if bdlsChain, ok := cs.Chain.(*Chain); ok {
		return bdlsChain
	}
	c.Logger.Warnf("BDLS ReceiverByChain: channel %s is of type %v, not *bdls.Chain", channelID, reflect.TypeOf(cs.Chain))
	return nil
}

// IsChannelMember inspects a join block and returns true iff this orderer's
// identity is in the channel's BDLS consenter set. Implementation is a
// straight port of smartbft.Consenter.IsChannelMember — the two consenters
// both identify members by TLS cert public-key equality, so diverging
// would just create two places for membership bugs to live.
func (c *Consenter) IsChannelMember(joinBlock *cb.Block) (bool, error) {
	if joinBlock == nil {
		return false, errors.New("nil block")
	}
	envelopeConfig, err := protoutil.ExtractEnvelope(joinBlock, 0)
	if err != nil {
		return false, err
	}
	bundle, err := channelconfig.NewBundleFromEnvelope(envelopeConfig, c.BCCSP)
	if err != nil {
		return false, err
	}
	oc, exists := bundle.OrdererConfig()
	if !exists {
		return false, errors.New("no orderer config in bundle")
	}

	sanitizedSelf, err := crypto.SanitizeX509Cert(c.Identity)
	if err != nil {
		return false, err
	}
	selfBlock, _ := pem.Decode(sanitizedSelf)
	if selfBlock == nil {
		return false, errors.Errorf("node identity certificate is not a valid PEM: %s", string(sanitizedSelf))
	}
	selfPub, err := cluster.ExtractPublicKeyFromCert(selfBlock.Bytes)
	if err != nil {
		c.Logger.Warnf("BDLS IsChannelMember: failed to extract own public key: %v", err)
		return false, err
	}

	for _, co := range oc.Consenters() {
		sanitized, err := crypto.SanitizeX509Cert(co.Identity)
		if err != nil {
			c.Logger.Warnf("BDLS IsChannelMember: failed to sanitize consenter %d: %v", co.Id, err)
			return false, err
		}
		block, _ := pem.Decode(sanitized)
		if block == nil {
			c.Logger.Warnf("BDLS IsChannelMember: consenter %d cert is not valid PEM", co.Id)
			continue
		}
		pub, err := cluster.ExtractPublicKeyFromCert(block.Bytes)
		if err != nil {
			c.Logger.Warnf("BDLS IsChannelMember: consenter %d public key extract failed: %v", co.Id, err)
			continue
		}
		if bytes.Equal(selfPub, pub) {
			return true, nil
		}
	}
	return false, nil
}

// detectSelfID finds this orderer's position in the channel's BDLS
// consenter set by matching the cluster TLS public key we loaded in
// New() against each consenter's ServerTlsCert. The returned id is the
// zero-based index into md.Consenters; BDLS itself derives the numeric
// participant id from the (X, Y) public-key coordinates via
// DefaultPubKeyToIdentity and does not actually need this integer, so
// it is used only for logging and for cluster.RPC routing in C7c.
//
// Note: we intentionally compare *public keys*, not sanitized-cert
// bytes. IsChannelMember above does the cert-bytes comparison because
// it operates against the orderer's MSP identity (which is the Fabric-
// wide convention for "is this orderer a member of this channel"); but
// for BDLS participant identity, the cluster TLS keypair is the source
// of truth — see signer.go for the full rationale. A node that
// IsChannelMember says "yes" to should still find itself here as long
// as the operator kept their MSP cert and cluster TLS cert consistent
// inside ConfigMetadata.Consenters, which is the ConsenterMapping
// convention the configtx encoder enforces.
func (c *Consenter) detectSelfID(md *bdlsproto.ConfigMetadata) (uint64, error) {
	if c.TLSPublicKey == nil {
		return 0, errors.New("consenter has no cluster TLS public key (loadClusterTLSPrivateKey failed at startup?)")
	}
	for i, co := range md.Consenters {
		pub, err := publicKeyFromTLSCert(co.ServerTlsCert)
		if err != nil {
			c.Logger.Warnf("BDLS detectSelfID: consenter %d public key extract failed: %v", i, err)
			continue
		}
		if publicKeysEqual(c.TLSPublicKey, pub) {
			return uint64(i), nil
		}
	}
	return 0, errors.New("this orderer is not in the channel's BDLS consenter set")
}
