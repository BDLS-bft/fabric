package bdls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"fmt"

	// "log"
	"math/big"
	"sync"
	"time"

	"code.cloudfoundry.org/clock"
	bdls "github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric-lib-go/bccsp"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric/orderer/common/cluster"

	//"github.com/hyperledger/fabric/orderer/common/types"
	types2 "github.com/hyperledger/fabric/orderer/common/types"
	// "github.com/hyperledger/fabric/orderer/common/localconfig"

	//"github.com/hyperledger/fabric/orderer/common/msgprocessor"
	"github.com/hyperledger/fabric/orderer/consensus"

	"github.com/hyperledger/fabric/protoutil"

	//"go.uber.org/zap"
	//"google.golang.org/protobuf/proto"
	"github.com/BDLS-bft/bdls/crypto/btcec"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric/internal/pkg/comm"
	"github.com/hyperledger/fabric/orderer/common/localconfig"
	"github.com/pkg/errors"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// secp256k1 elliptic curve
var S256Curve elliptic.Curve = btcec.S256()

const (
	baseLatency               = 500 * time.Millisecond
	maxBaseLatency            = 10 * time.Second
	proposalCollectionTimeout = 3 * time.Second
	updatePeriod              = 20 * time.Millisecond
	resendPeriod              = 10 * time.Second
)

// RPC is used to mock the transport layer in tests.
type RPC interface {
	SendConsensus(dest uint64, msg *orderer.ConsensusRequest) error
	SendSubmit(dest uint64, request *orderer.SubmitRequest, report func(err error)) error
}

type Options struct {
	Clock             clock.Clock
	Consenters        []*cb.Consenter
	TickInterval      time.Duration
	portAddress       string
	MaxSizePerMsg     uint64
	MaxInflightBlocks int
	Metrics           *Metrics
	RPCTimeout        time.Duration
	BDLSid            uint64
	Logger            *flogging.FabricLogger
	TLSCert           []byte
	TLSPrivKey        []byte
	srvConf           comm.ServerConfig
	conf              *localconfig.TopLevel
	//from etcdraft
	//BlockMetadata *etcdraft.BlockMetadata
	// BlockMetadata and Consenters should only be modified while under lock
	// of bdlsChainLock
	//Consenters    map[uint64]*etcdraft.Consenter
}

// BlockPuller is used to pull blocks from other OSN
type BlockPuller interface {
	PullBlock(seq uint64) *cb.Block
	HeightsByEndpoints() (map[string]uint64, string, error)
	Close()
}

// CreateBlockPuller is a function to create BlockPuller on demand.
// It is passed into chain initializer so that tests could mock this.
type CreateBlockPuller func() (BlockPuller, error)

// Configurator is used to configure the communication layer
// when the chain starts.
type Configurator interface {
	Configure(channel string, newNodes []cluster.RemoteNode)
}

type submit struct {
	req *orderer.SubmitRequest
	//leader chan uint64
}

type apply struct {
	height uint64
	round  uint64
	state  bdls.State
}

// Chain implements consensus.Chain interface.
type BDLSChain struct {
	self         *Consenter
	bdlsId       uint64
	Channel      string
	rpc          RPC
	configurator Configurator
	lastHeight   uint64
	//BDLS
	consensus           *bdls.Consensus
	config              *bdls.Config
	consensusMessages   [][]byte      // all consensus message awaiting to be processed
	sync.Mutex                        // fields lock
	chConsensusMessages chan struct{} // notification of new consensus message

	submitC chan *submit
	applyC  chan apply
	haltC   chan struct{} // Signals to goroutines that the chain is halting
	doneC   chan struct{} // Closes when the chain halts
	startC  chan struct{} // Closes when the node is started
	readyC  chan Ready

	errorCLock sync.RWMutex
	errorC     chan struct{} // returned by Errored()

	logger  *flogging.FabricLogger
	support consensus.ConsenterSupport
	opts    Options

	lastBlock    *cb.Block
	createPuller CreateBlockPuller

	CryptoProvider bccsp.BCCSP

	Metrics *Metrics

	bdlsChainLock sync.RWMutex

	unreachableLock sync.RWMutex
	unreachable     map[uint64]struct{}

	statusReportMutex sync.Mutex
	consensusRelation types2.ConsensusRelation
	status            types2.Status

	configInflight bool // this is true when there is config block or ConfChange in flight
	blockInflight  int  // number of in flight blocks

	latency          time.Duration
	die              chan struct{}
	dieOnce          sync.Once
	msgCount         int64
	bytesCount       int64
	minLatency       time.Duration
	maxLatency       time.Duration
	totalLatency     time.Duration
	bdlsMetadataLock sync.RWMutex
	clock            clock.Clock // Tests can inject a fake clock

	updateTicker *time.Ticker // For 20ms BDLS Update() calls

	//TBD
	// ActiveNodes     atomic.Value
	//verifier *Verifier
	//assembler *Assembler
	// haltCallback func()
	// SignerSerializer   signerSerializer
	// PolicyManager    policies.Manager
	// WALDir string
	//Config           types.Configuration
	// clusterService *cluster.ClusterService
	// RuntimeConfig *atomic.Value
	// Comm               cluster.Communicator
}

// Order accepts a message which has been processed at a given configSeq.
func (c *BDLSChain) Order(env *cb.Envelope, configSeq uint64) error {
	c.Metrics.NormalProposalsReceived.Add(1)
	seq := c.support.Sequence()
	if configSeq < seq {
		c.logger.Warnf("Normal message was validated against %d, although current config seq has advanced (%d)", configSeq, seq)
		// No need to ProcessNormalMsg. this process must be in Ordered func
		/*if _, err := c.support.ProcessNormalMsg(env); err != nil {
			return errors.Errorf("bad normal message: %s", err)
		}*/
	}
	return c.submit(env, configSeq)
}

func (c *BDLSChain) submit(env *cb.Envelope, configSeq uint64) error {

	/*if err := c.isRunning(); err != nil {
		c.Metrics.ProposalFailures.Add(1)
		return err
	}*/
	req := &orderer.SubmitRequest{LastValidationSeq: configSeq, Payload: env, Channel: c.Channel}

	select {
	case c.submitC <- &submit{req}:
		return nil
	case <-c.doneC:
		c.Metrics.ProposalFailures.Add(1)
		return errors.Errorf("chain is stopped")
	}

}

// Configure accepts a message which reconfigures the channel
func (c *BDLSChain) Configure(env *cb.Envelope, configSeq uint64) error {
	c.Metrics.ConfigProposalsReceived.Add(1)
	seq := c.support.Sequence()
	if configSeq < seq {
		c.logger.Warnf("Normal message was validated against %d, although current config seq has advanced (%d)", configSeq, seq)
		if configEnv, _, err := c.support.ProcessConfigMsg(env); err != nil {
			return errors.Errorf("bad normal message: %s", err)
		} else {
			return c.submit(configEnv, configSeq)
		}
	}
	return c.submit(env, configSeq)
}

// WaitReady blocks waiting for consenter to be ready for accepting new messages.
func (c *BDLSChain) WaitReady() error {
	c.logger.Infof("Waiting for BDLS chain %s to be ready", c.Channel)
	if err := c.isRunning(); err != nil {
		return err
	}

	select {
	case c.submitC <- nil:
	case <-c.doneC:
		return errors.Errorf("chain is stopped")
	}
	return nil
}

func (c *BDLSChain) isRunning() error {
	c.logger.Infof("Checking if BDLS chain %s is running", c.Channel)
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

// Errored returns a channel that closes when the chain stops.
func (c *BDLSChain) Errored() <-chan struct{} {
	c.logger.Infof("Errored Returning error channel for BDLS chain %s", c.bdlsId)
	c.errorCLock.RLock()
	defer c.errorCLock.RUnlock()
	return c.errorC
}

func (c *BDLSChain) Start() {
	// TODO: Implement this
	c.logger.Infof("Starting BDLS chain for channel %s with node ID %d", c.Channel, c.bdlsId)

	if err := c.configureComm(); err != nil {
		c.logger.Errorf("Failed to start chain, aborting: +%v", err)
		close(c.doneC)
		return
	}

	var err error
	c.consensus, err = bdls.NewConsensus(c.config)
	if err != nil {
		c.logger.Errorf("Failed to create BDLS consensus: %v", err)
		close(c.doneC)
		return
	}
	c.consensus.SetLatency(200 * time.Millisecond)
	c.lastHeight, _, _ = c.consensus.CurrentState()

	// Close startup channels (like BDLS PoC)
	close(c.startC)
	close(c.errorC)

	// Start the goroutines
	go c.startConsensus()
	go c.run()

}

func (c *BDLSChain) startConsensus() {
	logger := c.logger.With("method", "startConsensus")
	logger.Infof("Starting BDLS consensus for channel %s with node ID %d", c.Channel, c.bdlsId)
	// 20ms ticker as recommended in the paper
	c.updateTicker = time.NewTicker(20 * time.Millisecond)
	defer c.updateTicker.Stop()

	for {
		select {
		case <-c.updateTicker.C:
			if err := c.consensus.Update(time.Now()); err != nil {
				logger.Errorf("BDLS consensus update failed: %v", err)
			}

			height, round, state := c.consensus.CurrentState()
			if height > c.lastHeight {
				c.lastHeight = height
				c.applyC <- apply{
					state:  state,
					height: height,
					round:  round,
				}
			}

		case <-c.doneC:
			return
		}
	}
}

func (c *BDLSChain) run() {
	logger := c.logger.With("method", "run")
	logger.Infof("Starting BDLS chain run loop for channel %s", c.Channel)

	// Initialize batch timer (similar to Raft)
	ticking := false
	timer := c.clock.NewTimer(time.Second)
	if !timer.Stop() {
		<-timer.C()
	}

	startTimer := func() {
		if !ticking {
			ticking = true
			timer.Reset(c.support.SharedConfig().BatchTimeout())
		}
	}

	stopTimer := func() {
		if !timer.Stop() && ticking {
			<-timer.C()
		}
		ticking = false
	}

	// Block creator for all BDLS nodes (no leader/follower distinction)
	bc := &blockCreator{
		hash:   protoutil.BlockHeaderHash(c.lastBlock.Header),
		number: c.lastBlock.Header.Number,
		logger: c.logger,
	}

	logger.Infof("Start accepting requests at block [%d]", c.lastBlock.Header.Number)

	// Goroutine for block proposals to BDLS consensus
	proposalChannel := make(chan *cb.Block, c.opts.MaxInflightBlocks)
	go func() {
		for {
			select {
			case block := <-proposalChannel:
				data := protoutil.MarshalOrPanic(block)
				c.consensus.Propose(data) // Propose to BDLS consensus
				logger.Debugf("Proposed block [%d] to BDLS consensus", block.Header.Number)
			case <-c.doneC:
				return
			}
		}
	}()

	submitC := c.submitC

	for {
		select {
		case s := <-submitC:
			if s == nil {
				continue
			}

			// Process transaction batches
			batches, pending, err := c.ordered(s.req)
			if err != nil {
				logger.Errorf("Failed to order message: %s", err)
				continue
			}

			if !pending && len(batches) == 0 {
				continue
			}

			if pending {
				startTimer()
			} else {
				stopTimer()
			}

			// Propose blocks to BDLS
			c.propose(proposalChannel, bc, batches...)

			// Flow control
			if c.configInflight {
				logger.Info("Received config transaction, pause accepting transaction till it is committed")
				submitC = nil
			} else if c.blockInflight >= c.opts.MaxInflightBlocks {
				logger.Debugf("Number of in-flight blocks (%d) reaches limit (%d), pause accepting transaction",
					c.blockInflight, c.opts.MaxInflightBlocks)
				submitC = nil
			}

		case app := <-c.applyC:
			logger.Debugf("Applying consensus decision: height=%d", app.height)

			// Apply the consensus decision
			c.apply(app.height, app.round, app.state)

			// Resume accepting transactions if conditions are met
			if c.configInflight {
				logger.Info("Config block in flight, pause accepting transaction")
				submitC = nil
			} else if c.blockInflight < c.opts.MaxInflightBlocks {
				submitC = c.submitC
			}

		case <-timer.C():
			ticking = false
			logger.Debugf("Batch timer expired, creating block")

			batch := c.support.BlockCutter().Cut()
			if len(batch) == 0 {
				logger.Warningf("Batch timer expired with no pending requests")
				continue
			}

			c.propose(proposalChannel, bc, batch)

		case <-c.doneC:
			stopTimer()
			close(c.errorC)
			logger.Infof("Stop serving requests")
			return
		}
	}
}

// Orders the envelope in the `msg` content. SubmitRequest.
// Returns
//
//	-- batches [][]*common.Envelope; the batches cut,
//	-- pending bool; if there are envelopes pending to be ordered,
//	-- err error; the error encountered, if any.
//
// It takes care of config messages as well as the revalidation of messages if the config sequence has advanced.
func (c *BDLSChain) ordered(msg *orderer.SubmitRequest) (batches [][]*cb.Envelope, pending bool, err error) {
	seq := c.support.Sequence()

	isconfig, err := c.isConfig(msg.Payload)
	if err != nil {
		return nil, false, errors.Errorf("bad message: %s", err)
	}

	if isconfig {
		// ConfigMsg
		if msg.LastValidationSeq < seq {
			c.logger.Warnf("Config message was validated against %d, although current config seq has advanced (%d)", msg.LastValidationSeq, seq)
			msg.Payload, _, err = c.support.ProcessConfigMsg(msg.Payload)
			if err != nil {
				//c.Metrics.ProposalFailures.Add(1)
				return nil, true, errors.Errorf("bad config message: %s", err)
			}
		}

		batch := c.support.BlockCutter().Cut()
		batches = [][]*cb.Envelope{}
		if len(batch) != 0 {
			batches = append(batches, batch)
		}
		batches = append(batches, []*cb.Envelope{msg.Payload})
		return batches, false, nil
	}
	// it is a normal message
	if msg.LastValidationSeq < seq {
		c.logger.Warnf("Normal message was validated against %d, although current config seq has advanced (%d)", msg.LastValidationSeq, seq)
		if _, err := c.support.ProcessNormalMsg(msg.Payload); err != nil {
			//c.Metrics.ProposalFailures.Add(1)
			return nil, true, errors.Errorf("bad normal message: %s", err)
		}
	}
	batches, pending = c.support.BlockCutter().Ordered(msg.Payload)
	return batches, pending, nil
}

func (c *BDLSChain) isConfig(env *cb.Envelope) (bool, error) {
	h, err := protoutil.ChannelHeader(env)
	if err != nil {
		c.logger.Errorf("failed to extract channel header from envelope")
		return false, err
	}

	return h.Type == int32(cb.HeaderType_CONFIG), nil
}

func (c *BDLSChain) propose(ch chan<- *cb.Block, bc *blockCreator, batches ...[]*cb.Envelope) {
	for _, batch := range batches {
		b := bc.createNextBlock(batch)
		c.logger.Infof("Created block [%d], there are %d blocks in flight", b.Header.Number, c.blockInflight)

		select {
		case ch <- b:
		default:
			c.logger.Panic("Programming error: limit of in-flight blocks does not properly take effect or block is proposed by follower")
		}

		// if it is config block, then we should wait for the commit of the block
		if protoutil.IsConfigBlock(b) {
			c.configInflight = true
		}

		c.blockInflight++
	}
}

func (c *BDLSChain) apply(height uint64, round uint64, state bdls.State) {

	newBlock := protoutil.UnmarshalBlockOrPanic(state)
	c.writeBlock(newBlock, 0)
	c.Metrics.CommittedBlockNumber.Set(float64(newBlock.Header.Number))
}

func (c *BDLSChain) writeBlock(block *cb.Block, index uint64) {
	c.logger.Infof("WWWWWWWWWWWWWWWWWWWWWWWWWWW writeBlock WWWWWWWWWWWWWWWWWWWWWWWWWWWW")
	if block.Header.Number > c.lastBlock.Header.Number+1 {
		c.logger.Panicf("Got block [%d], expect block [%d]", block.Header.Number, c.lastBlock.Header.Number+1)
	} else if block.Header.Number < c.lastBlock.Header.Number+1 {
		c.logger.Infof("Got block [%d], expect block [%d], this node was forced to catch up", block.Header.Number, c.lastBlock.Header.Number+1)
		return
	}

	if c.blockInflight > 0 {
		c.blockInflight-- // Reduce on All Orderer
	}
	c.lastBlock = block

	c.logger.Infof("Writing block [%d] (BDLS index: %d) to ledger", block.Header.Number, index)

	if protoutil.IsConfigBlock(block) {
		c.configInflight = false
		//c.writeConfigBlock(block, index)
		c.support.WriteConfigBlock(block, nil)
		return
	}

	c.support.WriteBlock(block, nil)
}

func (c *BDLSChain) Halt() {
	logger := c.logger.With("method", "Halt")
	logger.Info("Halting BDLS chain")

	select {
	case <-c.doneC:
		logger.Info("Chain already halted")
	default:
		close(c.doneC)
		logger.Info("Chain halted successfully")
	}
}

func (c *BDLSChain) stop() bool {
	return false // TODO: Implement this
}

// NewChain constructs a chain object.
func NewChain(
	self *Consenter,
	selfID uint64,
	support consensus.ConsenterSupport,
	opts Options,
	conf Configurator,
	rpc RPC,
	cryptoProvider bccsp.BCCSP,
	f CreateBlockPuller,
	metrics *Metrics,

) (*BDLSChain, error) {
	logger := opts.Logger.With("bdls", "channel", support.ChannelID(), "node", opts.BDLSid)
	logger.Infof("----------Creating BDLS chain for channel %s with node ID %d--------------------", support.ChannelID(), selfID)
	b := support.Block(support.Height() - 1)
	if b == nil {
		logger.Errorf("Failed to get last block for channel %s", support.ChannelID())
	}

	c := &BDLSChain{
		bdlsId:              selfID,
		Channel:             support.ChannelID(),
		rpc:                 rpc,
		configurator:        conf,
		chConsensusMessages: make(chan struct{}),
		applyC:              make(chan apply),
		submitC:             make(chan *submit),
		haltC:               make(chan struct{}),
		doneC:               make(chan struct{}),
		startC:              make(chan struct{}),
		errorC:              make(chan struct{}),
		readyC:              make(chan Ready),
		logger:              logger,
		support:             support,
		opts:                opts,
		lastBlock:           b,
		createPuller:        f,
		CryptoProvider:      cryptoProvider,
		Metrics: &Metrics{
			ClusterSize:             metrics.ClusterSize.With("channel", support.ChannelID()),
			CommittedBlockNumber:    metrics.CommittedBlockNumber.With("channel", support.ChannelID()),
			ActiveNodes:             metrics.ActiveNodes.With("channel", support.ChannelID()),
			IsLeader:                metrics.IsLeader.With("channel", support.ChannelID()),
			LeaderID:                metrics.LeaderID.With("channel", support.ChannelID()),
			NormalProposalsReceived: metrics.NormalProposalsReceived.With("channel", support.ChannelID()),
			ConfigProposalsReceived: metrics.ConfigProposalsReceived.With("channel", support.ChannelID()),
		},

		clock:             opts.Clock,
		consensusRelation: types2.ConsensusRelationConsenter,
		status:            types2.StatusActive,

		updateTicker: time.NewTicker(opts.TickInterval),
	}

	// Sets initial values for metrics
	c.Metrics.ClusterSize.Set(float64(len(c.opts.Consenters)))
	c.Metrics.IsLeader.Set(float64(0)) // all nodes start out as followers
	c.Metrics.ActiveNodes.Set(float64(0))
	c.Metrics.CommittedBlockNumber.Set(float64(c.lastBlock.Header.Number))

	config := &bdls.Config{
		Epoch:         time.Now(),
		CurrentHeight: c.lastBlock.Header.Number, //support.Height() - 1, //0,
		StateCompare:  func(a bdls.State, b bdls.State) int { return bytes.Compare(a, b) },
		StateValidate: func(bdls.State) bool { return true },
	}

	Keys := make([]string, 0)
	Keys = append(Keys,
		"68082493172628484253808951113461196766221768923883438540199548009461479956986",
		"44652770827640294682875208048383575561358062645764968117337703282091165609211",
		"80512969964988849039583604411558290822829809041684390237207179810031917243659",
		"55978351916851767744151875911101025920456547576858680756045508192261620541580")
	for k := range Keys {
		i := new(big.Int)
		_, err := fmt.Sscan(Keys[k], i)
		if err != nil {
			c.logger.Warnf("error scanning value:", err)
		}
		priv := new(ecdsa.PrivateKey)
		priv.PublicKey.Curve = bdls.S256Curve
		priv.D = i
		priv.PublicKey.X, priv.PublicKey.Y = bdls.S256Curve.ScalarBaseMult(priv.D.Bytes())
		// myself
		if int(c.bdlsId) == k+1 {
			config.PrivateKey = priv
		}
		config.Participants = append(config.Participants, bdls.DefaultPubKeyToIdentity(&priv.PublicKey))
	}

	c.config = config

	disseminator := &Disseminator{RPC: c.rpc}
	disseminator.UpdateMetadata(nil) // initialize

	logger.Infof("BDLS is now serving chain %s", support.ChannelID())

	return c, nil
}

type Ready struct {
	state bdls.State
}

func (c *BDLSChain) configureComm() error {
	// Reset unreachable map when communication is reconfigured
	c.logger.Infof("Configuring communication for BDLS chain %s", c.Channel)
	nodes, err := c.remotePeers()
	if err != nil {
		return err
	}

	c.configurator.Configure(c.Channel, nodes)

	c.logger.Infof("BDLS chain %s communication configured", c.Channel)
	for i, consenter := range c.opts.Consenters {
		if uint64(i) == c.bdlsId {
			continue
		}

		peer := &BDLSPeer{
			nodeID:   uint64(i),
			endpoint: fmt.Sprintf("%s:%d", consenter.Host, consenter.Port),
			channel:  c.Channel,
			config:   c.config,
			rpc:      c.rpc,
			logger:   c.logger.With("peer", i),
		}

		// Create and attach gRPC connection
		conn, err := dialPeer(consenter, c.opts.conf)
		if err != nil {
			c.logger.Errorf("Failed to dial peer %d: %v", i, err)
			continue
		}
		peer.conn = conn

		if !c.consensus.Join(peer) {
			c.logger.Warnf("Failed to join peer %d", i)
		}
	}
	c.logger.Infof("BDLS chain %s communication configured", c.Channel)
	return nil
}

func dialPeer(consenter *cb.Consenter, conf *localconfig.TopLevel) (*grpc.ClientConn, error) {
	logger := flogging.MustGetLogger("bdls.consenter.d")
	var dialOpts []grpc.DialOption
	if conf.General.TLS.Enabled {
		creds, err := credentials.NewClientTLSFromFile(conf.General.TLS.Certificate, "")
		if err != nil {
			return nil, err
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	} else {
		logger.Errorf("Insecure connection to %s:%d", consenter.Host, consenter.Port)
	}
	return grpc.NewClient(fmt.Sprintf("%s:%d", consenter.Host, consenter.Port), dialOpts...)
}

func (c *BDLSChain) remotePeers() ([]cluster.RemoteNode, error) {
	c.bdlsMetadataLock.RLock()
	defer c.bdlsMetadataLock.RUnlock()

	var nodes []cluster.RemoteNode
	for bdlsID, consenter := range c.opts.Consenters {
		// No need to know yourself
		if uint64(bdlsID) == c.bdlsId {
			continue
		}
		serverCertAsDER, err := pemToDER(consenter.ServerTlsCert, uint64(bdlsID), "server", c.logger)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		clientCertAsDER, err := pemToDER(consenter.ClientTlsCert, uint64(bdlsID), "client", c.logger)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		nodes = append(nodes, cluster.RemoteNode{
			NodeAddress: cluster.NodeAddress{
				ID:       uint64(bdlsID),
				Endpoint: fmt.Sprintf("%s:%d", consenter.Host, consenter.Port),
			},
			NodeCerts: cluster.NodeCerts{
				ServerTLSCert: serverCertAsDER,
				ClientTLSCert: clientCertAsDER,
			},
		})
	}
	return nodes, nil
}
