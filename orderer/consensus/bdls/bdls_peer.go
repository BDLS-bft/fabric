package bdls

import (
	// "context"
	"crypto/ecdsa"
	"net"

	// "time"
	"sync"

	bdls "github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/pkg/errors"
	grpc "google.golang.org/grpc"
	// "github.com/hyperledger/fabric/orderer/common/cluster"
)

type BDLSPeer struct {
	nodeID   uint64
	endpoint string
	channel  string // Add channel name
	conn     *grpc.ClientConn
	logger   *flogging.FabricLogger // Add logger
	mutex    sync.RWMutex           // Add mutex for thread safety
	config   *bdls.Config
	rpc      RPC
}

func (p *BDLSPeer) GetPublicKey() *ecdsa.PublicKey {
	p.logger.Infof("GetPublicKey() Lock")
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	p.logger.Infof("Getting public key for peer %d on channel %s", p.nodeID, p.channel)
	if p.config == nil {
		return nil
	}
	return &p.config.PrivateKey.PublicKey
}

func (p *BDLSPeer) RemoteAddr() net.Addr {
	p.logger.Infof("RemoteAddr() Lock")
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	p.logger.Infof("Getting remote address for peer %d on channel %s", p.nodeID, p.channel)
	if p.conn != nil {
		state := p.conn.GetState()
		if state.String() != "READY" {
			addr, _ := net.ResolveTCPAddr("tcp", p.endpoint)
			return addr
		}

		// For established connections, parse the target
		target := p.conn.Target()
		addr, _ := net.ResolveTCPAddr("tcp", target)
		return addr
	}

	// Fallback: parse endpoint directly
	addr, _ := net.ResolveTCPAddr("tcp", p.endpoint)
	return addr
}

func (p *BDLSPeer) Send(msg []byte) error {
	p.logger.Infof("Send() Lock")
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	if p.rpc == nil {
		return errors.New("RPC interface not initialized")
	}

	if len(msg) == 0 {
		return errors.New("empty message")
	}

	req := &orderer.ConsensusRequest{
		Channel: p.channel,
		Payload: msg,
	}

	// Use the RPC interface pattern like disseminator.go
	err := p.rpc.SendConsensus(p.nodeID, req)
	if err != nil {
		p.logger.Errorf("Failed to send consensus message to peer %d: %v", p.nodeID, err)
		return errors.Wrapf(err, "failed to send to peer %d", p.nodeID)
	}

	p.logger.Debugf("Successfully sent message to peer %d on channel %s", p.nodeID, p.channel)
	return nil
}
