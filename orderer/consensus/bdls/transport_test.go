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
	"sync/atomic"
	"testing"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mock clusterRPC
// ---------------------------------------------------------------------------

type mockClusterRPC struct {
	calls []mockSendCall
	err   error // if non-nil, SendConsensus returns this
}

type mockSendCall struct {
	Destination uint64
	Channel     string
	Payload     []byte
}

func (m *mockClusterRPC) SendConsensus(dest uint64, msg *orderer.ConsensusRequest) error {
	m.calls = append(m.calls, mockSendCall{
		Destination: dest,
		Channel:     msg.Channel,
		Payload:     msg.Payload,
	})
	return m.err
}

func (m *mockClusterRPC) SendSubmit(_ uint64, _ *orderer.SubmitRequest, report func(err error)) error {
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
	rpc := &mockClusterRPC{}
	p, err := newPeerAdapter(
		flogging.MustGetLogger("test"),
		rpc, "testchan", 7, testPubKey(t), "localhost", 8080,
	)
	require.NoError(t, err)

	payload := []byte("bdls-signed-proto")
	require.NoError(t, p.Send(payload))
	require.Len(t, rpc.calls, 1)
	require.Equal(t, uint64(7), rpc.calls[0].Destination)
	require.Equal(t, "testchan", rpc.calls[0].Channel)
	require.Equal(t, payload, rpc.calls[0].Payload)
	require.Zero(t, p.sendErrorCount())
}

func TestPeerAdapter_Send_Error(t *testing.T) {
	rpc := &mockClusterRPC{err: fmt.Errorf("connection refused")}
	p, err := newPeerAdapter(
		flogging.MustGetLogger("test"),
		rpc, "testchan", 7, testPubKey(t), "localhost", 8080,
	)
	require.NoError(t, err)

	require.Error(t, p.Send([]byte("msg")))
	require.EqualValues(t, 1, p.sendErrorCount())

	require.Error(t, p.Send([]byte("msg2")))
	require.EqualValues(t, 2, p.sendErrorCount())
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
