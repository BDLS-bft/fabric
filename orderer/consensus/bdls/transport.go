/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"crypto/ecdsa"
	"fmt"
	"net"
	"sync/atomic"

	bdlslib "github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
)

// ---------------------------------------------------------------------------
// transport.go adapts Fabric's orderer/common/cluster.RPC to BDLS's
// PeerInterface. One peerAdapter is created per remote consenter per
// channel: BDLS itself treats each instance as a "peer", hands it
// pre-serialised SignedProto bytes via Send, and we relay those bytes over
// the existing cluster ConsensusRequest stream.
//
// The important consequence of this design is that BDLS does NOT open a
// second gRPC listener. Inbound messages still arrive through the
// orderer's existing Dispatcher → RequestHandler path (wired in the C5
// dispatcher commit) and land on Consensus.ReceiveMessage. The cluster
// transport stays a single pipe that three consenters (etcdraft, BFT,
// BDLS) multiplex across.
// ---------------------------------------------------------------------------

// clusterRPC is the subset of *cluster.RPC our adapter needs. Isolating it
// in an interface lets unit tests stand up a mock without pulling in the
// whole cluster comm package.
type clusterRPC interface {
	SendConsensus(destination uint64, msg *orderer.ConsensusRequest) error
	SendSubmit(destination uint64, request *orderer.SubmitRequest, report func(err error)) error
}

// fabricAddr is a net.Addr implementation that reports a host:port pair.
// Kept unexported — BDLS only uses the address for logging.
type fabricAddr struct {
	network string
	address string
}

func (f fabricAddr) Network() string { return f.network }
func (f fabricAddr) String() string  { return f.address }

// peerAdapter satisfies bdlslib.PeerInterface on top of cluster.RPC. Each
// adapter targets exactly one remote consenter on exactly one channel; the
// chain run-loop constructs n−1 of them per channel and hands the set to
// bdlslib.NewConsensus via (*Consensus).Join.
type peerAdapter struct {
	logger *flogging.FabricLogger

	rpc       clusterRPC
	channelID string
	// destination is the numeric cluster consenter id, required by
	// cluster.RPC.SendConsensus to route the StepRequest. This matches
	// the id Fabric assigns in channelconfig.Consenters().
	destination uint64

	remotePub  *ecdsa.PublicKey
	remoteAddr net.Addr

	// sendErrs tracks the number of consecutive Send failures so the chain
	// can decide whether to escalate to a catch-up (via blockpuller). The
	// counter is reset by the chain when it observes a successful decide.
	sendErrs uint64
}

// newPeerAdapter constructs a peerAdapter. It validates that the remote
// public key is usable as a BDLS identity — BDLS will otherwise panic deep
// inside DefaultPubKeyToIdentity when the first message flows through.
func newPeerAdapter(
	logger *flogging.FabricLogger,
	rpc clusterRPC,
	channelID string,
	destination uint64,
	remotePub *ecdsa.PublicKey,
	host string,
	port uint32,
) (*peerAdapter, error) {
	if rpc == nil {
		return nil, fmt.Errorf("bdls transport: rpc is nil")
	}
	if channelID == "" {
		return nil, fmt.Errorf("bdls transport: channelID is empty")
	}
	if remotePub == nil || remotePub.X == nil || remotePub.Y == nil {
		return nil, fmt.Errorf("bdls transport: remote public key for consenter %d is nil or uninitialised", destination)
	}
	return &peerAdapter{
		logger:      logger,
		rpc:         rpc,
		channelID:   channelID,
		destination: destination,
		remotePub:   remotePub,
		remoteAddr: fabricAddr{
			network: "bdls-cluster",
			address: fmt.Sprintf("%s:%d", host, port),
		},
	}, nil
}

// GetPublicKey implements bdlslib.PeerInterface. The returned key is the
// one parsed from the consenter's server TLS cert at HandleChain time, so
// any channel config update that rotates the cert must go through
// Chain.Configure → chain rebuild, not an in-place mutation.
func (p *peerAdapter) GetPublicKey() *ecdsa.PublicKey {
	return p.remotePub
}

// RemoteAddr implements bdlslib.PeerInterface.
func (p *peerAdapter) RemoteAddr() net.Addr {
	return p.remoteAddr
}

// Send implements bdlslib.PeerInterface. It wraps the BDLS-serialised
// SignedProto bytes into a cluster.ConsensusRequest and hands them to the
// existing orderer cluster stream. We never crack open the payload here —
// the wire format is owned by the BDLS library, not the transport.
func (p *peerAdapter) Send(msg []byte) error {
	if p.logger != nil {
		p.logger.Debugf("bdls peerAdapter.Send: sending msg to %d, len %d", p.destination, len(msg))
	}
	err := p.rpc.SendConsensus(p.destination, &orderer.ConsensusRequest{
		Channel: p.channelID,
		Payload: msg,
	})
	if err != nil {
		atomic.AddUint64(&p.sendErrs, 1)
		if p.logger != nil {
			p.logger.Debugf("bdls: SendConsensus to consenter %d on channel %s failed: %v",
				p.destination, p.channelID, err)
		}
		return err
	}
	return nil
}

// SendSubmit sends a SubmitRequest over the existing cluster stream.
func (p *peerAdapter) SendSubmit(msg *orderer.SubmitRequest) {
	if p.logger != nil {
		p.logger.Debugf("bdls peerAdapter.SendSubmit: sending msg to %d", p.destination)
	}
	_ = p.rpc.SendSubmit(p.destination, msg, func(err error) {
		if err != nil && p.logger != nil {
			p.logger.Debugf("bdls: SendSubmit to consenter %d on channel %s failed: %v",
				p.destination, p.channelID, err)
		}
	})
}

// resetSendErrs is called by chain.go on a successful decide so the
// consecutive-error count does not accumulate across healthy heights.
func (p *peerAdapter) resetSendErrs() { atomic.StoreUint64(&p.sendErrs, 0) }

// sendErrorCount lets the chain observe the running consecutive error
// count without taking a lock.
func (p *peerAdapter) sendErrorCount() uint64 { return atomic.LoadUint64(&p.sendErrs) }

// Compile-time guarantee the adapter satisfies BDLS's PeerInterface.
var _ bdlslib.PeerInterface = (*peerAdapter)(nil)
