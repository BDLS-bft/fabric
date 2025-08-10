package bdls

import (
	"sync"
	"sync/atomic"
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
	"github.com/hyperledger/fabric/orderer/common/localconfig"
	//"github.com/hyperledger/fabric/orderer/common/msgprocessor"
	"github.com/hyperledger/fabric/orderer/consensus"

	//"github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	//"go.uber.org/zap"
	//"google.golang.org/protobuf/proto"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric/common/policies"
)

type signerSerializer interface {
	// Sign a message and return the signature over the digest, or error on failure
	Sign(message []byte) ([]byte, error)

	// Serialize converts an identity to bytes
	Serialize() ([]byte, error)
}

// ConfigValidator interface
type ConfigValidator interface {
	ValidateConfig(env *cb.Envelope) error
}

// RPC is used to mock the transport layer in tests.
type RPC interface {
	SendConsensus(dest uint64, msg *orderer.ConsensusRequest) error
	SendSubmit(dest uint64, request *orderer.SubmitRequest, report func(err error)) error
}

type Options struct {
	//BlockMetadata *etcdraft.BlockMetadata
	Clock clock.Clock
	// BlockMetadata and Consenters should only be modified while under lock
	// of bdlsChainLock
	//Consenters    map[uint64]*etcdraft.Consenter
	Consenters []*cb.Consenter

	portAddress string

	MaxInflightBlocks int
	Metrics           *Metrics
	//from etcdraft
	RPCTimeout time.Duration
	BDLSid     uint64
	Logger     *flogging.FabricLogger
	TLSCert    []byte
}

// BDLSChain implements Chain interface to wire with
// BDLS library

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
	//height uint64
	//round  uint64
	state bdls.State
}

// Chain implements consensus.Chain interface.
type BDLSChain struct {
	bdlsId      uint64
	Channel     string
	rpc         RPC
	ActiveNodes atomic.Value

	//agent *agent

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

	errorCLock   sync.RWMutex
	errorC       chan struct{} // returned by Errored()
	haltCallback func()

	logger  *flogging.FabricLogger
	support consensus.ConsenterSupport
	//verifier *Verifier
	opts Options

	lastBlock *cb.Block
	//TBD
	RuntimeConfig *atomic.Value

	//Config           types.Configuration
	BlockPuller      BlockPuller
	Comm             cluster.Communicator
	SignerSerializer signerSerializer
	PolicyManager    policies.Manager

	WALDir string

	clusterService *cluster.ClusterService

	//assembler *Assembler
	Metrics *Metrics
	bccsp   bccsp.BCCSP

	bdlsChainLock sync.RWMutex

	unreachableLock sync.RWMutex
	unreachable     map[uint64]struct{}

	statusReportMutex sync.Mutex
	consensusRelation types2.ConsensusRelation
	status            types2.Status

	configInflight bool // this is true when there is config block or ConfChange in flight
	blockInflight  int  // number of in flight blocks

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
	c.errorCLock.RLock()
	defer c.errorCLock.RUnlock()
	return c.errorC
}

// Start should allocate whatever resources are needed for staying up to date with the chain.
// Typically, this involves creating a thread which reads from the ordering source, passes those
// messages to a block cutter, and writes the resulting blocks to the ledger.
// func (c *BDLSChain) Start() {
// 	c.Logger.Infof("Starting BDLS node")

// 	close(c.startC)
// 	close(c.errorC)

// 	go c.startConsensus(c.config)
// 	go c.run()

// }

// Start instructs the orderer to begin serving the chain and keep it current.
func (c *BDLSChain) Start() {
	// c.logger.Infof("Starting BDLS node")

	// if err := c.configureComm(); err != nil {
	// 	c.logger.Errorf("Failed to start chain, aborting: +%v", err)
	// 	close(c.doneC)
	// 	return
	// }

	// isJoin := c.support.Height() > 1
	// if isJoin && c.opts.MigrationInit {
	// 	isJoin = false
	// 	c.logger.Infof("Consensus-type migration detected, starting new raft node on an existing channel; height=%d", c.support.Height())
	// }
	// c.Node.start(c.fresh, isJoin)

	// close(c.startC)
	// close(c.errorC)

	// go c.gc()
	// go c.run()

	// es := c.newEvictionSuspector()

	// interval := DefaultLeaderlessCheckInterval
	// if c.opts.LeaderCheckInterval != 0 {
	// 	interval = c.opts.LeaderCheckInterval
	// }

	// c.periodicChecker = &PeriodicCheck{
	// 	Logger:        c.logger,
	// 	Report:        es.confirmSuspicion,
	// 	ReportCleared: es.clearSuspicion,
	// 	CheckInterval: interval,
	// 	Condition:     c.suspectEviction,
	// }
	// c.periodicChecker.Run()
}

// Halt stops the chain.
func (c *BDLSChain) Halt() {
	c.stop()
}

func (c *BDLSChain) stop() bool {
	select {
	case <-c.startC:
	default:
		c.logger.Warn("Attempted to halt a chain that has not started")
		return false
	}

	select {
	case c.haltC <- struct{}{}:
	case <-c.doneC:
		return false
	}
	<-c.doneC

	c.statusReportMutex.Lock()
	defer c.statusReportMutex.Unlock()
	c.status = types2.StatusInactive

	return true
}

// NewChain constructs a chain object.
func NewChain(
	selfID uint64,
	clusterDialer *cluster.PredicateDialer,
	localConfigCluster localconfig.Cluster,
	comm cluster.Communicator,
	signerSerializer signerSerializer,
	policyManager policies.Manager,
	metrics *Metrics,
	support consensus.ConsenterSupport,
	opts Options,
	conf Configurator,
	rpc RPC,
	cryptoProvider bccsp.BCCSP,
	f CreateBlockPuller,

) (*BDLSChain, error) {
	return nil, nil
}

type Ready struct {
	state bdls.State
}
