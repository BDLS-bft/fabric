/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"testing"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// test doubles
// ---------------------------------------------------------------------------

type stubReceiver struct {
	lastConsensusReq *orderer.ConsensusRequest
	lastSubmitReq    *orderer.SubmitRequest
	lastSender       uint64
}

func (s *stubReceiver) Consensus(req *orderer.ConsensusRequest, sender uint64) error {
	s.lastConsensusReq = req
	s.lastSender = sender
	return nil
}

func (s *stubReceiver) Submit(req *orderer.SubmitRequest, sender uint64) error {
	s.lastSubmitReq = req
	s.lastSender = sender
	return nil
}

type stubReceiverGetter struct {
	chains map[string]MessageReceiver
}

func (g *stubReceiverGetter) ReceiverByChain(channelID string) MessageReceiver {
	return g.chains[channelID]
}

// ---------------------------------------------------------------------------
// OnConsensus
// ---------------------------------------------------------------------------

func TestDispatcher_OnConsensus_HappyPath(t *testing.T) {
	recv := &stubReceiver{}
	d := &Dispatcher{
		Logger:        flogging.MustGetLogger("test"),
		ChainSelector: &stubReceiverGetter{chains: map[string]MessageReceiver{"mychan": recv}},
	}
	req := &orderer.ConsensusRequest{Channel: "mychan", Payload: []byte("data")}
	err := d.OnConsensus("mychan", 99, req)
	require.NoError(t, err)
	require.Equal(t, req, recv.lastConsensusReq)
	require.EqualValues(t, 99, recv.lastSender)
}

func TestDispatcher_OnConsensus_UnknownChannel(t *testing.T) {
	d := &Dispatcher{
		Logger:        flogging.MustGetLogger("test"),
		ChainSelector: &stubReceiverGetter{chains: map[string]MessageReceiver{}},
	}
	err := d.OnConsensus("ghost", 1, &orderer.ConsensusRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "doesn't exist")
}

// ---------------------------------------------------------------------------
// OnSubmit
// ---------------------------------------------------------------------------

func TestDispatcher_OnSubmit_HappyPath(t *testing.T) {
	recv := &stubReceiver{}
	d := &Dispatcher{
		Logger:        flogging.MustGetLogger("test"),
		ChainSelector: &stubReceiverGetter{chains: map[string]MessageReceiver{"ch": recv}},
	}
	req := &orderer.SubmitRequest{Channel: "ch"}
	err := d.OnSubmit("ch", 5, req)
	require.NoError(t, err)
	require.Equal(t, req, recv.lastSubmitReq)
	require.EqualValues(t, 5, recv.lastSender)
}

func TestDispatcher_OnSubmit_UnknownChannel(t *testing.T) {
	d := &Dispatcher{
		Logger:        flogging.MustGetLogger("test"),
		ChainSelector: &stubReceiverGetter{chains: map[string]MessageReceiver{}},
	}
	err := d.OnSubmit("ghost", 1, &orderer.SubmitRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "doesn't exist")
}
