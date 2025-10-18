/*
Copyright Ahmed Al Salih. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/clock"
	"github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric-protos-go/common"

	//cb "github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric-protos-go/orderer"
	"github.com/hyperledger/fabric/orderer/common/msgprocessor"

	types2 "github.com/hyperledger/fabric/orderer/common/types"

	"github.com/hyperledger/fabric-protos-go/msp"
	//"github.com/hyperledger/fabric-protos-go/orderer/etcdraft"
	legacyproto "github.com/golang/protobuf/proto"
	"github.com/hyperledger/fabric/bccsp"
	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/common/flogging"
	"github.com/hyperledger/fabric/common/policies"
	"github.com/hyperledger/fabric/orderer/common/cluster"
	"github.com/hyperledger/fabric/orderer/consensus"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"golang.org/x/crypto/sha3"
	"google.golang.org/protobuf/proto"

	"github.com/BDLS-bft/bdls/crypto/btcec"
)

// fabricSigner implements the bdls.Signer interface.
type fabricSigner struct {
	signer    signerSerializer
	publicKey *ecdsa.PublicKey
	logger    *flogging.FabricLogger
	hashFunc  func([]byte) []byte
	hashName  string
}

func newFabricSigner(signer signerSerializer, pubKey *ecdsa.PublicKey, logger *flogging.FabricLogger) *fabricSigner {
	fs := &fabricSigner{
		signer:    signer,
		publicKey: pubKey,
		logger:    logger,
	}
	fs.detectHashFunction()
	return fs
}

func (fs *fabricSigner) detectHashFunction() {
	if fs.publicKey == nil || fs.signer == nil {
		return
	}

	testDigest := []byte("fabric-bdls-signature-hash-probe")
	sigBytes, err := fs.signer.Sign(testDigest)
	if err != nil {
		if fs.logger != nil {
			fs.logger.Warnf("Failed to probe signer hash function: %v", err)
		}
		return
	}

	var sig struct {
		R, S *big.Int
	}
	if _, err := asn1.Unmarshal(sigBytes, &sig); err != nil {
		if fs.logger != nil {
			fs.logger.Warnf("Failed to decode probe signature: %v", err)
		}
		return
	}

	type candidate struct {
		name string
		fn   func([]byte) []byte
	}
	candidates := []candidate{
		{name: "identity", fn: func(in []byte) []byte { return in }},
		{name: "sha256", fn: func(in []byte) []byte {
			sum := sha256.Sum256(in)
			return sum[:]
		}},
		{name: "sha3-256", fn: func(in []byte) []byte {
			sum := sha3.Sum256(in)
			return sum[:]
		}},
	}

	for _, cand := range candidates {
		digest := cand.fn(testDigest)
		if ecdsa.Verify(fs.publicKey, digest, sig.R, sig.S) {
			if cand.name != "identity" {
				fs.hashFunc = cand.fn
				fs.hashName = cand.name
			} else {
				fs.hashFunc = nil
				fs.hashName = cand.name
			}
			if fs.logger != nil {
				fs.logger.Debugf("Detected signer hash function: %s", cand.name)
			}
			return
		}
	}

	if fs.logger != nil {
		fs.logger.Warnf("Unable to detect signer hash function; assuming identity")
	}
}

func (fs *fabricSigner) Sign(digest []byte) (r, s *big.Int, err error) {
	signature, err := fs.signer.Sign(digest)
	if err != nil {
		return nil, nil, err
	}

	var ecdsaSig struct {
		R, S *big.Int
	}

	_, err = asn1.Unmarshal(signature, &ecdsaSig)
	if err != nil {
		return nil, nil, err
	}

	if fs.logger != nil {
		expected := fs.HashDigest(digest)
		if !ecdsa.Verify(fs.publicKey, expected, ecdsaSig.R, ecdsaSig.S) {
			fs.logger.Warnf("Local signature verification failed (hash=%s)", fs.hashName)
		}
	}

	return ecdsaSig.R, ecdsaSig.S, nil
}

func (fs *fabricSigner) PublicKey() *ecdsa.PublicKey {
	return fs.publicKey
}

func (fs *fabricSigner) HashDigest(digest []byte) []byte {
	if fs.hashFunc == nil {
		return digest
	}
	return fs.hashFunc(digest)
}

// ConfigValidator interface
type ConfigValidator interface {
	ValidateConfig(env *common.Envelope) error
}

type BlockPuller interface {
	PullBlock(seq uint64) *common.Block
	HeightsByEndpoints() (map[string]uint64, error)
	Close()
}

// secp256k1 elliptic curve
var S256Curve elliptic.Curve = btcec.S256()

const (
	baseLatency               = 500 * time.Millisecond
	maxBaseLatency            = 10 * time.Second
	proposalCollectionTimeout = 3 * time.Second
	resendPeriod              = 10 * time.Second
	defaultTickInterval       = 20 * time.Millisecond
)

type signerSerializer interface {
	// Sign a message and return the signature over the digest, or error on failure
	Sign(message []byte) ([]byte, error)

	// Serialize converts an identity to bytes
	Serialize() ([]byte, error)
}

type submit struct {
	env       *common.Envelope
	configSeq uint64
	isConfig  bool
	result    chan error
}

type apply struct {
	//height uint64
	//round  uint64
	state bdls.State
}

// bdlsEgress implements the bdls.Transmitter interface.
type bdlsEgress struct {
	Channel     string
	SelfID      uint64
	RPC         *cluster.RPC
	Logger      *flogging.FabricLogger
	Consenters  func() []*common.Consenter
	IdentityMap map[bdls.Identity]uint64
}

// Broadcast sends a message to all remote nodes.
func (e *bdlsEgress) Broadcast(msg []byte) {
	signed := &bdls.SignedProto{}
	if err := proto.Unmarshal(msg, signed); err != nil {
		e.Logger.Warnf("Failed to decode BDLS consensus message for broadcast: %v", err)
		return
	}
	req := &orderer.ConsensusRequest{
		Payload: protoutil.MarshalOrPanic(signed),
		Channel: e.Channel,
	}

	consenters := e.Consenters()
	for _, consenter := range consenters {
		destID := uint64(consenter.Id)
		if destID == e.SelfID {
			continue
		}

		err := e.RPC.SendConsensus(destID, req)
		if err != nil {
			e.Logger.Warnf("Failed to send consensus message to %d: %s", destID, err)
		}
	}
}

// SendTo sends a message to a specific remote node.
func (e *bdlsEgress) SendTo(targetID bdls.Identity, msg []byte) {
	destID, ok := e.IdentityMap[targetID]
	if !ok {
		e.Logger.Warnf("Could not find Fabric node ID for BDLS identity %v", targetID)
		return
	}

	signed := &bdls.SignedProto{}
	if err := proto.Unmarshal(msg, signed); err != nil {
		e.Logger.Warnf("Failed to decode BDLS consensus message for %d: %v", destID, err)
		return
	}

	req := &orderer.ConsensusRequest{
		Payload: protoutil.MarshalOrPanic(signed),
		Channel: e.Channel,
	}

	if err := e.RPC.SendConsensus(destID, req); err != nil {
		e.Logger.Warnf("Failed to send consensus message to %d: %s", destID, err)
	}
}

// Chain represents a BDLS chain.
type Chain struct {
	bdlsId  uint64
	Channel string

	ActiveNodes atomic.Value

	//agent *agent

	//BDLS
	consensus    *bdls.Consensus
	config       *bdls.Config
	sync.Mutex   // fields lock
	submitC      chan *submit
	messageC     chan []byte
	requestC     chan []byte
	batchTimerCh chan struct{}
	haltC        chan struct{} // Signals to goroutines that the chain is halting
	doneC        chan struct{} // Closes when the chain halts
	startC       chan struct{} // Closes when the node is started

	errorCLock    sync.RWMutex
	errorC        chan struct{} // returned by Errored()
	errorSignaled bool
	haltCallback  func()

	Logger   *flogging.FabricLogger
	support  consensus.ConsenterSupport
	verifier *Verifier
	opts     Options

	lastBlock *common.Block
	//TBD
	RuntimeConfig *atomic.Value

	//Config           types.Configuration
	BlockPuller      BlockPuller
	Comm             cluster.Communicator
	SignerSerializer signerSerializer
	PolicyManager    policies.Manager

	WALDir string

	clusterService *cluster.ClusterService

	assembler *Assembler
	Metrics   *Metrics
	bccsp     bccsp.BCCSP

	identityMap    map[bdls.Identity]uint64
	signingPubKeys map[uint64]*ecdsa.PublicKey
	selfSigningKey *ecdsa.PublicKey

	bdlsChainLock sync.RWMutex

	unreachableLock sync.RWMutex
	unreachable     map[uint64]struct{}

	statusReportMutex sync.Mutex
	consensusRelation types2.ConsensusRelation
	status            types2.Status

	configInflight bool // this is true when there is config block or ConfChange in flight
	blockInflight  int  // number of in flight blocks

	batchTimeout time.Duration
	batchTimer   *time.Timer
	timerMutex   sync.Mutex

	latency      time.Duration
	die          chan struct{}
	dieOnce      sync.Once
	msgCount     int64
	bytesCount   int64
	minLatency   time.Duration
	maxLatency   time.Duration
	totalLatency time.Duration

	clock clock.Clock // Tests can inject a fake clock
}

type consensusTicker struct {
	ticker clock.Ticker
}

func (t *consensusTicker) Chan() <-chan time.Time {
	return t.ticker.C()
}

func (t *consensusTicker) Stop() {
	t.ticker.Stop()
}

type Options struct {
	//BlockMetadata *etcdraft.BlockMetadata
	Clock clock.Clock
	// BlockMetadata and Consenters should only be modified while under lock
	// of bdlsChainLock
	//Consenters    map[uint64]*etcdraft.Consenter
	Consenters []*common.Consenter

	portAddress string

	MaxInflightBlocks int
	TickInterval      time.Duration
}

// Order accepts a message which has been processed at a given configSeq.
func (c *Chain) Order(env *common.Envelope, configSeq uint64) error {
	if err := c.isRunning(); err != nil {
		c.Metrics.ProposalFailures.Add(1)
		return err
	}
	c.Metrics.NormalProposalsReceived.Add(1)
	return c.submit(env, configSeq, false)
}

// Configure accepts a message which reconfigures the channel
func (c *Chain) Configure(env *common.Envelope, configSeq uint64) error {
	if err := c.isRunning(); err != nil {
		c.Metrics.ProposalFailures.Add(1)
		return err
	}
	c.Metrics.ConfigProposalsReceived.Add(1)
	return c.submit(env, configSeq, true)
}

func (c *Chain) proposeBatch(batch []*common.Envelope) {
	block := c.support.CreateNextBlock(batch)
	data := protoutil.MarshalOrPanic(block)
	c.Logger.Infof("proposeBatch height=%d txs=%d", block.Header.Number, len(block.Data.Data))
	c.consensus.Propose(data)
}

func (c *Chain) submit(env *common.Envelope, configSeq uint64, isConfig bool) error {
	req := &submit{
		env:       env,
		configSeq: configSeq,
		isConfig:  isConfig,
		result:    make(chan error, 1),
	}

	select {
	case c.submitC <- req:
	case <-c.haltC:
		return errors.Errorf("chain is stopped")
	}

	return <-req.result
}

func (c *Chain) runLoop() {
	for {
		select {
		case req := <-c.submitC:
			if req == nil {
				continue
			}
			err := c.handleSubmission(req)
			req.result <- err
		case msg := <-c.messageC:
			if c.consensus == nil {
				continue
			}
			if err := c.consensus.ReceiveMessage(msg, time.Now()); err != nil {
				c.Logger.Warnf("ReceiveMessage failed: %v", err)
			}
		case payload := <-c.requestC:
			if c.consensus == nil {
				continue
			}
			c.consensus.SubmitRequest(payload, time.Now())
		case <-c.batchTimerCh:
			c.handleBatchTimeout()
		case <-c.haltC:
			for {
				select {
				case req := <-c.submitC:
					if req == nil {
						continue
					}
					req.result <- errors.Errorf("chain is stopped")
				default:
					select {
					case <-c.doneC:
					default:
						close(c.doneC)
					}
					return
				}
			}
		}
	}
}

func (c *Chain) handleSubmission(req *submit) error {
	batches, pending, err := c.ordered(req.env, req.configSeq)
	if err != nil {
		return err
	}

	if len(batches) == 0 && !pending {
		c.stopBatchTimer()
		return nil
	}

	for _, batch := range batches {
		c.proposeBatch(batch)
	}

	if req.isConfig {
		c.stopBatchTimer()
		return nil
	}

	if pending {
		c.startBatchTimer()
	} else {
		c.stopBatchTimer()
	}

	return nil
}

func (c *Chain) startBatchTimer() {
	if c.batchTimeout == 0 {
		return
	}

	c.timerMutex.Lock()
	defer c.timerMutex.Unlock()

	if c.batchTimer != nil {
		return
	}

	c.Logger.Debugf("Starting batch timer (%s)", c.batchTimeout)
	c.batchTimer = time.AfterFunc(c.batchTimeout, func() {
		c.timerMutex.Lock()
		c.batchTimer = nil
		c.timerMutex.Unlock()
		select {
		case c.batchTimerCh <- struct{}{}:
		default:
		}
	})
}

func (c *Chain) stopBatchTimer() {
	c.timerMutex.Lock()
	defer c.timerMutex.Unlock()

	if c.batchTimer == nil {
		return
	}

	if !c.batchTimer.Stop() {
		// Timer already fired; allow handler to proceed but avoid reusing timer.
	}
	c.batchTimer = nil
}

func (c *Chain) handleBatchTimeout() {
	if err := c.isRunning(); err != nil {
		return
	}

	c.Logger.Debugf("Batch timer expired; cutting pending batch")
	batch := c.support.BlockCutter().Cut()
	if len(batch) == 0 {
		c.Logger.Debugf("Batch timer expired with no pending requests")
		return
	}

	c.proposeBatch(batch)
}

func (c *Chain) newConsensusTicker(interval time.Duration) bdls.Ticker {
	if interval <= 0 {
		interval = defaultTickInterval
	}
	ticker := c.clock.NewTicker(interval)
	return &consensusTicker{ticker: ticker}
}

func (c *Chain) WaitReady() error {
	return nil
}

// Errored returns a channel which will close when an error has occurred.
func (c *Chain) Errored() <-chan struct{} {
	return c.errorC
}

func (c *Chain) signalError() {
	c.errorCLock.Lock()
	defer c.errorCLock.Unlock()
	if c.errorSignaled {
		return
	}
	c.errorSignaled = true
	close(c.errorC)
}

// NewChain creates new chain
func NewChain(
	//cv ConfigValidator,
	selfID uint64,
	//config types.Configuration,
	walDir string,
	blockPuller BlockPuller,
	comm cluster.Communicator,
	signerSerializer signerSerializer,
	policyManager policies.Manager,
	support consensus.ConsenterSupport,
	metrics *Metrics,
	bccsp bccsp.BCCSP,
	opts Options,

) (*Chain, error) {
	/*requestInspector := &RequestInspector{
		ValidateIdentityStructure: func(_ *msp.SerializedIdentity) error {
			return nil
		},
	}*/

	logger := flogging.MustGetLogger("orderer.consensus.bdls.chain").With(zap.String("channel", support.ChannelID()))
	//oldb := support.Block(support.Height() - 1)
	b := LastBlockFromLedgerOrPanic(support, logger)

	if b == nil {
		return nil, errors.Errorf("failed to get last block")
	}

	clk := opts.Clock
	if clk == nil {
		clk = clock.NewClock()
		opts.Clock = clk
	}

	tickInterval := opts.TickInterval
	if tickInterval <= 0 {
		tickInterval = defaultTickInterval
		opts.TickInterval = tickInterval
	}

	c := &Chain{
		SignerSerializer:  signerSerializer,
		Channel:           support.ChannelID(),
		lastBlock:         b,
		WALDir:            walDir,
		Comm:              comm,
		support:           support,
		PolicyManager:     policyManager,
		BlockPuller:       blockPuller,
		Logger:            logger,
		opts:              opts,
		bdlsId:            selfID,
		haltC:             make(chan struct{}),
		doneC:             make(chan struct{}),
		startC:            make(chan struct{}),
		errorC:            make(chan struct{}),
		clock:             clk,
		consensusRelation: types2.ConsensusRelationConsenter,
		status:            types2.StatusActive,
		Metrics: &Metrics{
			ClusterSize:             metrics.ClusterSize.With("channel", support.ChannelID()),
			CommittedBlockNumber:    metrics.CommittedBlockNumber.With("channel", support.ChannelID()),
			ActiveNodes:             metrics.ActiveNodes.With("channel", support.ChannelID()),
			IsLeader:                metrics.IsLeader.With("channel", support.ChannelID()),
			LeaderID:                metrics.LeaderID.With("channel", support.ChannelID()),
			NormalProposalsReceived: metrics.NormalProposalsReceived.With("channel", support.ChannelID()),
			ConfigProposalsReceived: metrics.ConfigProposalsReceived.With("channel", support.ChannelID()),
		},
		bccsp:        bccsp,
		identityMap:  make(map[bdls.Identity]uint64),
		submitC:      make(chan *submit),
		messageC:     make(chan []byte, 1024),
		requestC:     make(chan []byte, 1024),
		batchTimerCh: make(chan struct{}, 1),
	}
	c.batchTimeout = support.SharedConfig().BatchTimeout()

	// Sets initial values for metrics
	c.Metrics.ClusterSize.Set(float64(len(c.opts.Consenters)))
	c.Metrics.IsLeader.Set(float64(0)) // all nodes start out as followers
	c.Metrics.ActiveNodes.Set(float64(0))
	c.Metrics.CommittedBlockNumber.Set(float64(c.lastBlock.Header.Number))

	// Initialize a minimal verifier to validate incoming Submit requests
	requestInspector := &RequestInspector{
		ValidateIdentityStructure: func(_ *msp.SerializedIdentity) error { return nil },
	}
	c.verifier = &Verifier{
		RuntimeConfig:         &atomic.Value{},
		ReqInspector:          requestInspector,
		AccessController:      &chainACL{policyManager: policyManager, Logger: logger},
		VerificationSequencer: support,
		Logger:                logger,
		Ledger:                support,
	}

	selfSerializedIdentity, err := signerSerializer.Serialize()
	if err != nil {
		return nil, errors.Wrap(err, "failed to serialize signing identity")
	}

	selfSigningPubKey, err := publicKeyFromSerializedIdentity(selfSerializedIdentity, logger)
	if err != nil {
		return nil, errors.Wrap(err, "failed to extract signing public key")
	}
	logger.Infof("Self signing key X=%s Y=%s", hex.EncodeToString(selfSigningPubKey.X.Bytes()), hex.EncodeToString(selfSigningPubKey.Y.Bytes()))

	signer := newFabricSigner(signerSerializer, selfSigningPubKey, logger)

	rpc := &cluster.RPC{
		Channel:       c.Channel,
		Comm:          c.Comm,
		Logger:        c.Logger,
		StreamsByType: cluster.NewStreamsByType(),
		Timeout:       5 * time.Minute, // align with SmartBFT defaults
	}
	egress := &bdlsEgress{
		Channel: c.Channel,
		SelfID:  c.bdlsId,
		RPC:     rpc,
		Logger:  c.Logger,
		Consenters: func() []*common.Consenter {
			return c.opts.Consenters
		},
		IdentityMap: c.identityMap,
	}

	// setup consensus config at the given height
	config := &bdls.Config{
		Epoch:         time.Now(),
		CurrentHeight: c.lastBlock.Header.Number,
		StateCompare:  func(a bdls.State, b bdls.State) int { return bytes.Compare(a, b) },
		StateValidate: func(bdls.State) bool { return true },
		Comm:          egress,
		Deliver:       c.deliver,
		Logger:        c.Logger,
		Signer:        signer,
		TickInterval:  tickInterval,
		NewTicker:     c.newConsensusTicker,
	}

	id2Identities := make(NodeIdentitiesByID)
	nodeIDs := make([]uint64, 0, len(opts.Consenters))
	var selfPubKeyFromConfig *ecdsa.PublicKey
	for _, consenter := range opts.Consenters {
		nodeID := uint64(consenter.Id)
		nodeIDs = append(nodeIDs, nodeID)
		if len(consenter.Identity) > 0 {
			id2Identities[nodeID] = consenter.Identity
		}

		pubKey, err := publicKeyFromIdentity(consenter.Identity, logger)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse identity cert for consenter %d", consenter.Id)
		}
		logger.Infof("Consenter %d signing key X=%s Y=%s", nodeID, hex.EncodeToString(pubKey.X.Bytes()), hex.EncodeToString(pubKey.Y.Bytes()))

		identity := bdls.DefaultPubKeyToIdentity(pubKey)
		config.Participants = append(config.Participants, identity)
		c.identityMap[identity] = nodeID

		if nodeID == selfID {
			selfPubKeyFromConfig = pubKey
		}
	}

	if selfPubKeyFromConfig == nil {
		return nil, errors.Errorf("could not find self (%d) identity in consenters config", selfID)
	}

	if selfSigningPubKey.X.Cmp(selfPubKeyFromConfig.X) != 0 || selfSigningPubKey.Y.Cmp(selfPubKeyFromConfig.Y) != 0 {
		logger.Warnf("Signing identity public key does not match consenter identity for self; using signing identity")
	}

	c.config = config
	c.selfSigningKey = selfSigningPubKey
	c.signingPubKeys = make(map[uint64]*ecdsa.PublicKey)
	for _, consenter := range opts.Consenters {
		if len(consenter.Identity) == 0 {
			continue
		}
		pubKey, err := publicKeyFromIdentity(consenter.Identity, logger)
		if err != nil {
			continue
		}
		c.signingPubKeys[uint64(consenter.Id)] = pubKey
	}

	runtimeConfig := RuntimeConfig{
		consenters:             opts.Consenters,
		Nodes:                  nodeIDs,
		ID2Identities:          id2Identities,
		LastCommittedBlockHash: hex.EncodeToString(protoutil.BlockHeaderHash(b.Header)),
		LastBlock:              b,
		LastConfigBlock:        b,
	}

	nodes, err := c.remotePeers()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	runtimeConfig.RemoteNodes = nodes
	c.verifier.RuntimeConfig.Store(runtimeConfig)
	c.verifier.ConsenterVerifier = &consenterVerifier{
		logger:        logger,
		channel:       support.ChannelID(),
		policyManager: policyManager,
	}
	c.Comm.Configure(c.support.ChannelID(), nodes)

	logger.Infof("BDLS is now serving chain %s", support.ChannelID())

	return c, nil
}

// Halt frees the resources which were allocated for this Chain.
func (c *Chain) Halt() {
	c.stopBatchTimer()

	if cons := c.consensus; cons != nil {
		cons.Stop()
	}

	// Signal shutdown to background routines
	select {
	case <-c.haltC:
		// already closed
	default:
		close(c.haltC)
	}
}

// Get the remote peers from the []*cb.Consenter
func (c *Chain) remotePeers() ([]cluster.RemoteNode, error) {
	c.bdlsChainLock.RLock()
	defer c.bdlsChainLock.RUnlock()

	ordererConfig := c.support.SharedConfig()
	if ordererConfig == nil {
		return nil, errors.New("cannot get orderer config")
	}
	orgs := ordererConfig.Organizations()

	var tlsCACerts [][]byte
	for _, org := range orgs {
		tlsCACerts = append(tlsCACerts, org.MSP().GetTLSRootCerts()...)
	}

	var nodes []cluster.RemoteNode
	for _, consenter := range c.opts.Consenters {
		serverCertAsDER, err := pemToDER(consenter.ServerTlsCert, uint64(consenter.Id), "server", c.Logger)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		clientCertAsDER, err := pemToDER(consenter.ClientTlsCert, uint64(consenter.Id), "client", c.Logger)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		nodes = append(nodes, cluster.RemoteNode{
			NodeAddress: cluster.NodeAddress{
				ID:       uint64(consenter.Id),
				Endpoint: fmt.Sprintf("%s:%d", consenter.Host, consenter.Port),
			},
			NodeCerts: cluster.NodeCerts{
				ServerTLSCert: serverCertAsDER,
				ClientTLSCert: clientCertAsDER,
				ServerRootCA:  tlsCACerts,
				Identity:      consenter.Identity,
			},
		})
	}

	return nodes, nil
}

// HandleMessage handles the message from the sender
func (c *Chain) HandleMessage(sender uint64, m []byte) {
	if c.consensus == nil {
		c.Logger.Warnf("Consensus not initialized; dropping message from %d", sender)
		return
	}
	signed := &bdls.SignedProto{}
	if err := proto.Unmarshal(m, signed); err != nil {
		c.Logger.Warnf("Failed to decode BDLS signed message from %d: %v", sender, err)
	} else {
		msg := &bdls.Message{}
		if err := proto.Unmarshal(signed.Message, msg); err != nil {
			c.Logger.Warnf("Failed to decode BDLS message body from %d: %v", sender, err)
		} else {
			stateLen := 0
			if msg.State != nil {
				stateLen = len(msg.State)
			}
			c.Logger.Infof("Message from %d: type=%s height=%d round=%d stateLen=%d", sender, msg.Type.String(), msg.Height, msg.Round, stateLen)
		}
		identity := bdls.Identity{}
		xAxis := normalizeBDLSAxis(signed.X)
		yAxis := normalizeBDLSAxis(signed.Y)
		copy(identity[:bdls.SizeAxis], xAxis)
		copy(identity[bdls.SizeAxis:], yAxis)
		if nodeID, ok := c.identityMap[identity]; ok {
			c.Logger.Debugf("Resolved identity for sender %d as nodeID %d", sender, nodeID)
		} else {
			c.Logger.Warnf("Could not resolve identity for sender %d", sender)
		}
	}
	select {
	case c.messageC <- m:
	default:
		c.Logger.Warnf("Message queue full; dropping message from %d", sender)
	}
}

// HandleRequest handles the request from the sender
func (c *Chain) HandleRequest(sender uint64, req []byte) {
	c.Logger.Infof("HandleRequest from %d", sender)
	if c.consensus == nil {
		c.Logger.Warnf("Consensus not initialized; dropping request from %d", sender)
		return
	}
	if c.verifier == nil {
		c.Logger.Warnf("Verifier is not initialized; dropping request from %d", sender)
		return
	}
	if _, err := c.verifier.VerifyRequest(req); err != nil {
		c.Logger.Warnf("Got bad request from %d: %v", sender, err)
		return
	}
	select {
	case c.requestC <- req:
	default:
		c.Logger.Warnf("Request queue full; dropping request from %d", sender)
	}
}

func normalizeBDLSAxis(axis []byte) []byte {
	if len(axis) == bdls.SizeAxis {
		return axis
	}
	buf := make([]byte, bdls.SizeAxis)
	if len(axis) > bdls.SizeAxis {
		axis = axis[len(axis)-bdls.SizeAxis:]
	}
	copy(buf[bdls.SizeAxis-len(axis):], axis)
	return buf
}

func pemToDER(pemBytes []byte, id uint64, certType string, logger *flogging.FabricLogger) ([]byte, error) {
	bl, _ := pem.Decode(pemBytes)
	if bl == nil {
		logger.Errorf("Rejecting PEM block of %s TLS cert for node %d, offending PEM is: %s", certType, id, string(pemBytes))
		return nil, errors.Errorf("invalid PEM block")
	}
	return bl.Bytes, nil
}

// publicKeyFromCertificate returns the public key of the given ASN1 DER certificate.
func publicKeyFromCertificate(der []byte) ([]byte, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return x509.MarshalPKIXPublicKey(cert.PublicKey)
}

func publicKeyFromIdentity(identity []byte, logger *flogging.FabricLogger) (*ecdsa.PublicKey, error) {
	sanitized, err := crypto.SanitizeX509Cert(identity)
	if err != nil {
		return nil, errors.Wrap(err, "failed to sanitize identity certificate")
	}
	block, _ := pem.Decode(sanitized)
	if block == nil {
		return nil, errors.Errorf("failed to PEM decode identity certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse identity certificate")
	}
	pubKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.Errorf("identity certificate public key is not ECDSA")
	}
	return pubKey, nil
}

func publicKeyFromSerializedIdentity(serialized []byte, logger *flogging.FabricLogger) (*ecdsa.PublicKey, error) {
	sid := &msp.SerializedIdentity{}
	if err := legacyproto.Unmarshal(serialized, sid); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal serialized identity")
	}
	return publicKeyFromIdentity(sid.IdBytes, logger)
}

func (c *Chain) updateMembership(consenters []*common.Consenter) ([]cluster.RemoteNode, error) {
	newIdentityMap := make(map[bdls.Identity]uint64, len(consenters))
	newSigningKeys := make(map[uint64]*ecdsa.PublicKey, len(consenters))
	newParticipants := make([]bdls.Identity, 0, len(consenters))
	newNodeIDs := make([]uint64, 0, len(consenters))
	newID2Identities := make(NodeIdentitiesByID, len(consenters))

	for _, consenter := range consenters {
		nodeID := uint64(consenter.Id)
		newNodeIDs = append(newNodeIDs, nodeID)

		if len(consenter.Identity) > 0 {
			newID2Identities[nodeID] = consenter.Identity
		}

		pubKey, err := publicKeyFromIdentity(consenter.Identity, c.Logger)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse identity cert for consenter %d", consenter.Id)
		}

		identity := bdls.DefaultPubKeyToIdentity(pubKey)

		newParticipants = append(newParticipants, identity)
		newIdentityMap[identity] = nodeID
		newSigningKeys[nodeID] = pubKey
	}

	c.bdlsChainLock.Lock()
	c.opts.Consenters = consenters
	if c.config != nil {
		c.config.Participants = newParticipants
	}
	c.identityMap = newIdentityMap
	c.signingPubKeys = newSigningKeys
	c.bdlsChainLock.Unlock()

	nodes, err := c.remotePeers()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	runtimeConfig := c.verifier.RuntimeConfig.Load().(RuntimeConfig)
	runtimeConfig.consenters = consenters
	runtimeConfig.Nodes = newNodeIDs
	runtimeConfig.ID2Identities = newID2Identities
	runtimeConfig.RemoteNodes = nodes
	c.verifier.RuntimeConfig.Store(runtimeConfig)

	return nodes, nil
}

// Orders the envelope in the `msg` content. SubmitRequest.
// Returns
//
//	-- batches [][]*common.Envelope; the batches cut,
//	-- pending bool; if there are envelopes pending to be ordered,
//	-- err error; the error encountered, if any.
//
// It takes care of config messages as well as the revalidation of messages if the config sequence has advanced.
func (c *Chain) ordered(env *common.Envelope, configSeq uint64) (batches [][]*common.Envelope, pending bool, err error) {
	seq := c.support.Sequence()
	if configSeq < seq {
		c.Logger.Warnf("Normal message was validated against %d, although current config seq has advanced (%d)", configSeq, seq)
		if _, err := c.support.ProcessNormalMsg(env); err != nil {
			return nil, false, errors.Errorf("bad normal message: %s", err)
		}
	}

	isconfig, err := c.isConfig(env)
	if err != nil {
		return nil, false, errors.Errorf("bad message: %s", err)
	}

	if isconfig {
		// ConfigMsg
		if configEnv, _, err := c.support.ProcessConfigMsg(env); err != nil {
			return nil, false, errors.Errorf("bad config message: %s", err)
		} else {
			batch := c.support.BlockCutter().Cut()
			batches = [][]*common.Envelope{}
			if len(batch) != 0 {
				batches = append(batches, batch)
			}
			batches = append(batches, []*common.Envelope{configEnv})
			return batches, false, nil
		}
	}
	// it is a normal message
	batches, pending = c.support.BlockCutter().Ordered(env)
	return batches, pending, nil
}

func (c *Chain) writeBlock(block *common.Block, index uint64) {
	c.Logger.Infof("WWWWWWWWWWWWWWWWWWWWWWWWWWW writeBlock WWWWWWWWWWWWWWWWWWWWWWWWWWWW")
	if block.Header.Number > c.lastBlock.Header.Number+1 {
		c.Logger.Panicf("Got block [%d], expect block [%d]", block.Header.Number, c.lastBlock.Header.Number+1)
	} else if block.Header.Number < c.lastBlock.Header.Number+1 {
		c.Logger.Infof("Got block [%d], expect block [%d], this node was forced to catch up", block.Header.Number, c.lastBlock.Header.Number+1)
		return
	}

	if c.blockInflight > 0 {
		c.blockInflight-- // Reduce on All Orderer
	}
	c.lastBlock = block

	c.Logger.Infof("Writing block [%d] (BDLS index: %d) to ledger", block.Header.Number, index)

	if protoutil.IsConfigBlock(block) {
		c.configInflight = false
		c.support.WriteConfigBlock(block, nil)

		newConsenters := c.support.SharedConfig().Consenters()
		nodes, err := c.updateMembership(newConsenters)
		if err != nil {
			c.Logger.Panicf("Failed to update membership from config block: %s", err)
		}

		runtimeConfig := c.verifier.RuntimeConfig.Load().(RuntimeConfig)
		runtimeConfig.LastConfigBlock = block
		runtimeConfig.LastBlock = block
		runtimeConfig.LastCommittedBlockHash = hex.EncodeToString(protoutil.BlockHeaderHash(block.Header))
		runtimeConfig.RemoteNodes = nodes
		c.verifier.RuntimeConfig.Store(runtimeConfig)

		if err := c.configureComm(); err != nil {
			c.Logger.Panicf("Failed to configure communication: %s", err)
		}
		return
	}

	c.support.WriteBlock(block, nil)

	runtimeConfig := c.verifier.RuntimeConfig.Load().(RuntimeConfig)
	runtimeConfig.LastBlock = block
	runtimeConfig.LastCommittedBlockHash = hex.EncodeToString(protoutil.BlockHeaderHash(block.Header))
	c.verifier.RuntimeConfig.Store(runtimeConfig)
}

func (c *Chain) configureComm() error {
	// Reset unreachable map when communication is reconfigured
	c.unreachableLock.Lock()
	c.unreachable = make(map[uint64]struct{})
	c.unreachableLock.Unlock()

	nodes, err := c.remotePeers()
	if err != nil {
		return err
	}

	//c.configurator.Configure(c.channelID, nodes)
	c.Comm.Configure(c.support.ChannelID(), nodes)
	return nil
}

func (c *Chain) isConfig(env *common.Envelope) (bool, error) {
	h, err := protoutil.ChannelHeader(env)
	if err != nil {
		c.Logger.Errorf("failed to extract channel header from envelope")
		return false, err
	}

	return h.Type == int32(common.HeaderType_CONFIG), nil
}

func (c *Chain) isRunning() error {
	select {
	case <-c.startC:
	default:
		return errors.Errorf("chain is not started")
	}

	select {
	case <-c.doneC:
		return errors.Errorf("chain is stopped")
	default:
	}

	return nil
}

// Start should allocate whatever resources are needed for staying up to date with the chain.
// Typically, this involves creating a thread which reads from the ordering source, passes those
// messages to a block cutter, and writes the resulting blocks to the ledger.
func (c *Chain) Start() {
	c.Logger.Infof("Starting BDLS node")

	if err := c.startConsensus(c.config); err != nil {
		c.Logger.Errorf("Failed to start BDLS consensus: %v", err)
		c.signalError()
		return
	}

	close(c.startC)

	go c.runLoop()
}

// consensus for one round with full procedure
func (c *Chain) startConsensus(config *bdls.Config) error {

	// create consensus
	consensus, err := bdls.NewConsensus(config)
	if err != nil {
		return errors.Wrap(err, "cannot create BDLS consensus")
	}
	c.consensus = consensus

	if err := c.consensus.Start(); err != nil {
		c.consensus = nil
		return errors.Wrap(err, "cannot start BDLS consensus ticker")
	}

	go c.TestMultiClients()

	// The run() loop will now drive consensus events.
	return nil
}

func (c *Chain) deliver(state bdls.State) error {
	if len(state) == 0 {
		c.Logger.Warnf("deliver called with empty state; skipping write")
		return nil
	}

	newBlock := protoutil.UnmarshalBlockOrPanic(state)
	c.Logger.Debugf("deliver state len=%d blockNum=%d", len(state), newBlock.Header.Number)
	c.writeBlock(newBlock, 0)
	c.Metrics.CommittedBlockNumber.Set(float64(newBlock.Header.Number))
	return nil
}

// StatusReport returns the ConsensusRelation & Status
func (c *Chain) StatusReport() (types2.ConsensusRelation, types2.Status) {
	c.statusReportMutex.Lock()
	defer c.statusReportMutex.Unlock()

	return c.consensusRelation, c.status
}

type chainACL struct {
	policyManager policies.Manager
	Logger        *flogging.FabricLogger
}

// Evaluate evaluates signed data
func (c *chainACL) Evaluate(signatureSet []*protoutil.SignedData) error {
	policy, ok := c.policyManager.GetPolicy(policies.ChannelWriters)
	if !ok {
		return fmt.Errorf("could not find policy %s", policies.ChannelWriters)
	}

	err := policy.EvaluateSignedData(signatureSet)
	if err != nil {
		c.Logger.Debugf("SigFilter evaluation failed: %s, policyName: %s", err.Error(), policies.ChannelWriters)
		return errors.Wrap(errors.WithStack(msgprocessor.ErrPermissionDenied), err.Error())
	}
	return nil
}
