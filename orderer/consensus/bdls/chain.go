package bdls

import (
	"fmt"
	"time"

	bdlslib "github.com/BDLS-bft/bdls"
	gogoproto "github.com/gogo/protobuf/proto"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/orderer/common/blockcutter"
	"github.com/hyperledger/fabric/orderer/consensus"
	"github.com/hyperledger/fabric/protoutil"
)

type Chain struct {
    haltC    chan struct{}
    doneC    chan struct{}
    erroredC chan struct{}
    support  consensus.ConsenterSupport
    Channel  string
    consensus *bdlslib.Consensus
    bc       blockcutter.Receiver
    submitC  chan *cb.Envelope
    Logger   *flogging.FabricLogger
    inFlightBlocks int
    totalTPSProcessed int
}

func newChain() *Chain {
	return &Chain{
		haltC:    make(chan struct{}),
		doneC:    make(chan struct{}),
		erroredC: make(chan struct{}),
		submitC:  make(chan *cb.Envelope, 1000),
	}
}

func (c *Chain) Order(env *cb.Envelope, configSeq uint64) error {
    select {
    case c.submitC <- env:
    case <-c.doneC:
        return fmt.Errorf("chain stopped")
    }
    return nil
}

func (c *Chain) Configure(config *cb.Envelope, configSeq uint64) error {
    return c.Order(config, configSeq)
}

func (c *Chain) WaitReady() error {
    select {
    case <-c.doneC:
        return fmt.Errorf("chain is stopped")
    default:
        return nil
    }
}

func (c *Chain) Errored() <-chan struct{} {
    return c.erroredC
}

// Start launches the BDLS chain processing loop
// It will periodically tick the BDLS consensus and, upon height advance,
// create and write blocks to the ledger.
func (c *Chain) Start() {
    go func() {
        fmt.Println("*********Starting BDLS Chain*********")
		go c.TestMultiClients()
        if c.Logger != nil {
            c.Logger.Infof("*********Starting BDLS Chain********* channel=%s", c.Channel)
        }

        var lastHeight uint64
        for {
            select {
            case <-c.haltC:
                close(c.doneC)
                return
            case env := <-c.submitC:
                if c.consensus == nil {
                    continue
                }
                if c.bc != nil {
                    batches, _ := c.bc.Ordered(env)
                    for _, batch := range batches {
                        if len(batch) == 0 {
                            continue
                        }
                        // Serialize full batch into BlockData for BDLS state
                        bd := &cb.BlockData{Data: make([][]byte, 0, len(batch))}
                        for _, e := range batch {
                            bd.Data = append(bd.Data, protoutil.MarshalOrPanic(e))
                        }
                        if c.Logger != nil {
                            c.Logger.Debugf("func3 -> Proposed block batch (%d envelopes) to BDLS consensus channel=%s", len(batch), c.Channel)
                        }
                        c.inFlightBlocks++
                        if payload, err := gogoproto.Marshal(bd); err == nil {
                            c.consensus.Propose(bdlslib.State(payload))
                        }
                    }
                } else {
                    // No block cutter; wrap single envelope in BlockData and propose
                    bd := &cb.BlockData{Data: [][]byte{protoutil.MarshalOrPanic(env)}}
                    if c.Logger != nil {
                        c.Logger.Debugf("func3 -> Proposed block (single envelope) to BDLS consensus channel=%s", c.Channel)
                    }
                    c.inFlightBlocks++
                    if payload, err := gogoproto.Marshal(bd); err == nil {
                        c.consensus.Propose(bdlslib.State(payload))
                    }
                }
            default:
            }
            if c.consensus != nil {
                _ = c.consensus.Update(time.Now())
                h, _, data := c.consensus.CurrentState()
                if c.Logger != nil { c.Logger.Debugf("state -> h=%d last=%d data=%t channel=%s", h, lastHeight, data != nil, c.Channel) }
                if h > lastHeight && data != nil {
                    // Data holds a serialized cb.BlockData with a batch of envelopes
                    var bd cb.BlockData
                    if err := gogoproto.Unmarshal([]byte(data), &bd); err == nil {
                        envs := make([]*cb.Envelope, 0, len(bd.Data))
                        for _, raw := range bd.Data {
                            var env cb.Envelope
                            if err := gogoproto.Unmarshal(raw, &env); err == nil {
                                envs = append(envs, &env)
                            }
                        }
                        if len(envs) > 0 {
                            // Convert apiv2 common.Envelope pointers to the exact type expected by support
                            // The consensus package imports the same apiv2 package; construct a slice of that type explicitly
                            converted := make([]*cb.Envelope, 0, len(envs))
                            for _, e := range envs {
                                converted = append(converted, e)
                            }
                            blk := c.support.CreateNextBlock(converted)
                            if c.Logger != nil {
                                c.Logger.Infof("writeBlock -> Writing block [%d] (BDLS index: %d) to ledger channel=%s", blk.Header.Number, 0, c.Channel)
                            }
                            // Attach BDLS proof as ORDERER metadata (temporary encoding)
                            if proof := c.consensus.CurrentProof(); proof != nil {
                                if meta, err := gogoproto.Marshal(proof); err == nil {
                                    c.support.WriteBlock(blk, meta)
                                } else {
                                    c.support.WriteBlock(blk, nil)
                                }
                            } else {
                                c.support.WriteBlock(blk, nil)
                            }
                            if c.Logger != nil {
                                c.Logger.Debugf("commitBlock -> Wrote block [%d] channel=%s", blk.Header.Number, c.Channel)
                            }
                            // TPS reporting based on total processed envelopes since test start
                            c.totalTPSProcessed += len(envs)
                            if !startTime.IsZero() {
                                elapsed := time.Since(startTime).Seconds()
                                if elapsed > 0 {
                                    tps := float64(c.totalTPSProcessed) / elapsed
                                    if c.Logger != nil {
                                        c.Logger.Infof("commitBlock ->  **************************************** The Total time is %vs , The TPS value is %f", elapsed, tps)
                                    }
                                }
                            }
                            if c.inFlightBlocks > 0 {
                                c.inFlightBlocks--
                            }
                            if c.Logger != nil {
                                c.Logger.Infof("run -> Start accepting requests at block [%d] channel=%s", blk.Header.Number, c.Channel)
                            }
                        } else {
                            // No valid envelopes, still advance ledger to avoid stalling
                            blk := c.support.CreateNextBlock(nil)
                            c.support.WriteBlock(blk, nil)
                        }
                    }
                    lastHeight = h
                }
            }
            time.Sleep(20 * time.Millisecond)
        }
    }()
}

func (c *Chain) Halt() {
    select {
    case <-c.doneC:
        return
    default:
    }
    close(c.haltC)
    close(c.doneC)
}

// Implement the BDLS-specific MessageReceiver minimal interface used by ingress.
func (c *Chain) ReceiveMessage(sender uint64, payload []byte) {
    c.Logger.Info("We are here")
    if c.consensus == nil {
        return
    }
    _ = sender
    _ = c.consensus.ReceiveMessage(payload, time.Now())
}

func (c *Chain) SubmitRequest(sender uint64, payload []byte) {
    if c.consensus == nil {
        return
    }
    _ = sender
    c.consensus.Propose(bdlslib.State(payload))
}
