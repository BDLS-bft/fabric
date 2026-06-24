/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/pkg/errors"
)

// ---------------------------------------------------------------------------
// dispatcher.go routes inbound cluster messages to the per-channel BDLS
// Chain. The shape is deliberately identical to
// orderer/consensus/etcdraft/dispatcher.go:
//
//   * MessageReceiver is implemented by *Chain in chain.go.
//   * ReceiverGetter is implemented by *Consenter (via ReceiverByChain)
//     once Phase C7 lands.
//   * Dispatcher itself is a tiny fan-out that the cluster gRPC service
//     (RegisterClusterNodeServiceServer in consenter.go) points at.
//
// We intentionally keep the types separate from the etcdraft ones —
// orderer/common/server/main.go wires a per-consenter Dispatcher today,
// so sharing the type would force a cross-package dependency between
// etcdraft and bdls that buys nothing. Identical contract, distinct
// types.
// ---------------------------------------------------------------------------

// MessageReceiver is what per-channel BDLS chains expose to the Dispatcher
// for inbound routing. Both methods must be safe to call concurrently — the
// cluster service may fan out messages on multiple goroutines.
type MessageReceiver interface {
	// Consensus hands a ConsensusRequest (which carries a
	// BDLS-serialised SignedProto in its Payload) to the chain for
	// delivery to Consensus.ReceiveMessage.
	Consensus(req *orderer.ConsensusRequest, sender uint64) error

	// Submit hands a SubmitRequest (a client envelope) to the chain so
	// it can flow through the block-cutter and into a proposal.
	Submit(req *orderer.SubmitRequest, sender uint64) error
}

// ReceiverGetter is the Consenter-side hook the Dispatcher uses to find
// the per-channel chain. The Consenter's ReceiverByChain reads from the
// Registrar and returns nil if the channel is not (yet) known to this
// orderer — the Dispatcher surfaces that as "channel doesn't exist".
type ReceiverGetter interface {
	ReceiverByChain(channelID string) MessageReceiver
}

// Dispatcher is the thin bridge between the cluster gRPC service and the
// per-channel BDLS chains. It owns no state of its own beyond the chain
// selector and a logger.
type Dispatcher struct {
	Logger        *flogging.FabricLogger
	ChainSelector ReceiverGetter
}

// OnConsensus is invoked by the cluster service when a remote orderer
// sends us a StepRequest_ConsensusRequest. The sender id is derived
// upstream from the caller's TLS identity, so we do not re-validate it
// here.
func (d *Dispatcher) OnConsensus(channel string, sender uint64, request *orderer.ConsensusRequest) error {
	recv := d.ChainSelector.ReceiverByChain(channel)
	if recv == nil {
		if d.Logger != nil {
			d.Logger.Warningf("bdls dispatcher: ConsensusRequest for unknown channel %q from sender %d", channel, sender)
		}
		return errors.Errorf("channel %s doesn't exist", channel)
	}
	err := recv.Consensus(request, sender)
	if err != nil && d.Logger != nil {
		d.Logger.Errorf("bdls dispatcher: recv.Consensus failed for channel %q from sender %d: %v", channel, sender, err)
	}
	return err
}

// OnSubmit is invoked by the cluster service when a remote orderer
// forwards us a client transaction via StepRequest_SubmitRequest.
func (d *Dispatcher) OnSubmit(channel string, sender uint64, request *orderer.SubmitRequest) error {
	recv := d.ChainSelector.ReceiverByChain(channel)
	if recv == nil {
		if d.Logger != nil {
			d.Logger.Warningf("bdls dispatcher: SubmitRequest for unknown channel %q from sender %d", channel, sender)
		}
		return errors.Errorf("channel %s doesn't exist", channel)
	}
	err := recv.Submit(request, sender)
	if err != nil && d.Logger != nil {
		d.Logger.Errorf("bdls dispatcher: recv.Submit failed for channel %q from sender %d: %v", channel, sender, err)
	}
	return err
}
