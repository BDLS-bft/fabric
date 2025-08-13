package bdls

import (
	"crypto/ecdsa"
	"net"
	"strconv"

	ab "github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric/orderer/common/cluster"
)

// fabricPeer implements BDLS PeerInterface over Fabric cluster RPC.
type fabricPeer struct{
    id  uint64
    rpc *cluster.RPC
    pub *ecdsa.PublicKey
}

func (p *fabricPeer) GetPublicKey() *ecdsa.PublicKey { return p.pub }
type idAddr struct{ id uint64 }
func (idAddr) Network() string          { return "cluster" }
func (a idAddr) String() string         { return strconv.FormatUint(a.id, 10) }

func (p *fabricPeer) RemoteAddr() net.Addr          { return idAddr{id: p.id} }
func (p *fabricPeer) Send(payload []byte) error {
    if p.rpc == nil {
        return nil
    }
    // Wrap BDLS payload in ConsensusRequest
    req := &ab.ConsensusRequest{
        Channel: p.rpc.Channel,
        Payload: payload,
    }
    // Best-effort broadcast; destination is encoded in 'id', but this PeerInterface API doesn't pass it
    // The network manager will provide per-peer instances with dedicated rpc + id.
    return p.rpc.SendConsensus(p.id, req)
}

// fabricNetworkManager maps consenters to remote nodes and exposes broadcast helper.
type fabricNetworkManager struct{
    comm cluster.Communicator
    rpc  *cluster.RPC
}

func (m *fabricNetworkManager) Configure(channel string, nodes []cluster.RemoteNode) {
    if m.rpc != nil {
        m.rpc.Channel = channel
    }
    if m.comm != nil {
        m.comm.Configure(channel, nodes)
    }
}

func (m *fabricNetworkManager) Broadcast(channel string, payload []byte) {
    // Intended for future use if we maintain a set of peers to broadcast to
    _ = channel
    _ = payload
}


