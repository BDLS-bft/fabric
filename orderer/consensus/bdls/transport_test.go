/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mock clusterRPC
// ---------------------------------------------------------------------------

type mockClusterRPC struct {
	mu          sync.Mutex
	calls       []mockSendCall
	submitCalls []mockSubmitCall
	err         error // if non-nil, SendConsensus/SendSubmit returns this
	callC       chan mockSendCall
}

type mockSendCall struct {
	Destination uint64
	Channel     string
	Payload     []byte
}

type mockSubmitCall struct {
	Destination uint64
	Channel     string
	Payload     []byte
}

func (m *mockClusterRPC) SendConsensus(dest uint64, msg *orderer.ConsensusRequest) error {
	call := mockSendCall{
		Destination: dest,
		Channel:     msg.Channel,
		Payload:     msg.Payload,
	}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()
	if m.callC != nil {
		m.callC <- call
	}
	return m.err
}

func (m *mockClusterRPC) SendSubmit(dest uint64, req *orderer.SubmitRequest, report func(err error)) error {
	call := mockSubmitCall{Destination: dest}
	if req != nil {
		call.Channel = req.Channel
		if req.Payload != nil {
			call.Payload = append([]byte(nil), req.Payload.Payload...)
		}
	}
	m.mu.Lock()
	m.submitCalls = append(m.submitCalls, call)
	m.mu.Unlock()
	if report != nil {
		report(m.err)
	}
	return m.err
}

// ---------------------------------------------------------------------------
// newPeerAdapter
// ---------------------------------------------------------------------------

func testPubKey(t *testing.T) *ecdsa.PublicKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return &priv.PublicKey
}

func TestNewPeerAdapter_HappyPath(t *testing.T) {
	rpc := &mockClusterRPC{}
	pub := testPubKey(t)
	p, err := newPeerAdapter(
		flogging.MustGetLogger("test"),
		rpc, "mychannel", 42, pub, "peer0.org1.example.com", 7050,
	)
	require.NoError(t, err)
	require.Equal(t, pub, p.GetPublicKey())
	require.Equal(t, "peer0.org1.example.com:7050", p.RemoteAddr().String())
	require.Equal(t, "bdls-cluster", p.RemoteAddr().Network())
}

func TestNewPeerAdapter_NilRPC(t *testing.T) {
	_, err := newPeerAdapter(nil, nil, "ch", 1, testPubKey(t), "h", 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rpc is nil")
}

func TestNewPeerAdapter_EmptyChannel(t *testing.T) {
	_, err := newPeerAdapter(nil, &mockClusterRPC{}, "", 1, testPubKey(t), "h", 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "channelID is empty")
}

func TestNewPeerAdapter_NilPubKey(t *testing.T) {
	_, err := newPeerAdapter(nil, &mockClusterRPC{}, "ch", 1, nil, "h", 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil or uninitialised")
}

// ---------------------------------------------------------------------------
// Send
// ---------------------------------------------------------------------------

func TestPeerAdapter_Send_HappyPath(t *testing.T) {
	rpc := &mockClusterRPC{callC: make(chan mockSendCall, 1)}
	p, err := newPeerAdapter(
		flogging.MustGetLogger("test"),
		rpc, "testchan", 7, testPubKey(t), "localhost", 8080,
	)
	require.NoError(t, err)
	defer p.stop()

	payload := []byte("bdls-signed-proto")
	require.NoError(t, p.Send(payload))
	call := waitForSendCall(t, rpc.callC)
	require.Equal(t, uint64(7), call.Destination)
	require.Equal(t, "testchan", call.Channel)
	require.Equal(t, payload, call.Payload)
	require.Zero(t, p.sendErrorCount())
}

func TestPeerAdapter_Send_Error(t *testing.T) {
	rpc := &mockClusterRPC{err: fmt.Errorf("connection refused"), callC: make(chan mockSendCall, 2)}
	p, err := newPeerAdapter(
		flogging.MustGetLogger("test"),
		rpc, "testchan", 7, testPubKey(t), "localhost", 8080,
	)
	require.NoError(t, err)
	defer p.stop()

	require.NoError(t, p.Send([]byte("msg")))
	waitForSendCall(t, rpc.callC)
	require.Eventually(t, func() bool {
		return p.sendErrorCount() == 1
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, p.Send([]byte("msg2")))
	waitForSendCall(t, rpc.callC)
	require.Eventually(t, func() bool {
		return p.sendErrorCount() == 2
	}, time.Second, 10*time.Millisecond)
}

func TestPeerAdapter_Send_PreservesOrder(t *testing.T) {
	rpc := &mockClusterRPC{callC: make(chan mockSendCall, 3)}
	p, err := newPeerAdapter(
		flogging.MustGetLogger("test"),
		rpc, "testchan", 7, testPubKey(t), "localhost", 8080,
	)
	require.NoError(t, err)
	defer p.stop()

	require.NoError(t, p.Send([]byte("one")))
	require.NoError(t, p.Send([]byte("two")))
	require.NoError(t, p.Send([]byte("three")))

	require.Equal(t, []byte("one"), waitForSendCall(t, rpc.callC).Payload)
	require.Equal(t, []byte("two"), waitForSendCall(t, rpc.callC).Payload)
	require.Equal(t, []byte("three"), waitForSendCall(t, rpc.callC).Payload)
}

// ---------------------------------------------------------------------------
// resetSendErrs / sendErrorCount
// ---------------------------------------------------------------------------

func TestPeerAdapter_ResetSendErrs(t *testing.T) {
	rpc := &mockClusterRPC{err: fmt.Errorf("fail")}
	p, err := newPeerAdapter(
		flogging.MustGetLogger("test"),
		rpc, "ch", 1, testPubKey(t), "h", 1,
	)
	require.NoError(t, err)

	// Accumulate some errors.
	atomic.StoreUint64(&p.sendErrs, 5)
	require.EqualValues(t, 5, p.sendErrorCount())

	p.resetSendErrs()
	require.Zero(t, p.sendErrorCount())
}

func waitForSendCall(t *testing.T, callC <-chan mockSendCall) mockSendCall {
	t.Helper()
	select {
	case call := <-callC:
		return call
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async send")
		return mockSendCall{}
	}
}
