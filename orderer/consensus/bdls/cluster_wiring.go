/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"encoding/pem"
	"fmt"

	"github.com/hyperledger/fabric-lib-go/bccsp"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/common/channelconfig"
	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/msp"
	"github.com/hyperledger/fabric/orderer/common/cluster"
	"github.com/hyperledger/fabric/orderer/consensus"

	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// cluster_wiring.go turns a Fabric ConsenterSupport into the cluster-layer
// primitives BDLS needs to plug into the shared orderer cluster transport:
//
//   * []cluster.RemoteNode — fed to cluster.AuthCommMgr.Configure(channel, …)
//     so outbound StepRequests can find each peer's TLS identity and dialer
//     config.
//
//   * []*common.Consenter — fed to cluster.ClusterService.ConfigureNodeCerts
//     (which BDLS shares with smartbft via main.go) so inbound StepRequests
//     are authenticated against the right per-channel member set.
//
// Both are derived from the channel's last config block. This mirrors the
// path smartbft takes in remoteNodesFromConfigBlock — deliberately, because
// having two cluster consenters compute different views of the same channel
// membership would be a Heisenbug factory. We do not import smartbft's
// unexported helper; the surface we need is small enough to replicate here
// and keeping this package free of a smartbft dependency matters more than
// the few duplicated lines.
// ---------------------------------------------------------------------------

// buildRemoteNodes walks a ConsenterSupport's ledger back to its last config
// block, parses the channel config bundle, and turns the orderer config's
// Consenters into cluster.RemoteNode + *common.Consenter pairs. The returned
// slices are index-aligned with oc.Consenters().
//
// The caller is expected to call this once per HandleChain invocation — one
// config-block parse per chain (re)initialisation matches smartbft's cadence
// and keeps the cluster layer quiescent between config updates.
func buildRemoteNodes(
	support consensus.ConsenterSupport,
	csp bccsp.BCCSP,
	logger *flogging.FabricLogger,
) ([]cluster.RemoteNode, []*cb.Consenter, error) {
	h := support.Height()
	if h == 0 {
		return nil, nil, errors.New("bdls cluster wiring: ledger height is 0, no config block to parse")
	}
	lastBlock := support.Block(h - 1)
	if lastBlock == nil {
		return nil, nil, errors.Errorf("bdls cluster wiring: ledger reports height %d but block %d is missing", h, h-1)
	}
	lastConfig, err := cluster.LastConfigBlock(lastBlock, blockRetrieverFromSupport{support})
	if err != nil {
		return nil, nil, errors.Wrap(err, "bdls cluster wiring: locating last config block")
	}
	if lastConfig == nil || lastConfig.Data == nil || len(lastConfig.Data.Data) == 0 {
		return nil, nil, errors.New("bdls cluster wiring: last config block has no envelopes")
	}

	env := &cb.Envelope{}
	if err := proto.Unmarshal(lastConfig.Data.Data[0], env); err != nil {
		return nil, nil, errors.Wrap(err, "bdls cluster wiring: unmarshalling config envelope")
	}
	bundle, err := channelconfig.NewBundleFromEnvelope(env, csp)
	if err != nil {
		return nil, nil, errors.Wrap(err, "bdls cluster wiring: building channel config bundle")
	}
	oc, ok := bundle.OrdererConfig()
	if !ok {
		return nil, nil, errors.New("bdls cluster wiring: no orderer config in bundle")
	}
	msps, err := bundle.MSPManager().GetMSPs()
	if err != nil {
		return nil, nil, errors.Wrap(err, "bdls cluster wiring: reading MSPs")
	}

	consenters := oc.Consenters()
	if len(consenters) == 0 {
		return nil, nil, errors.New("bdls cluster wiring: channel has zero consenters")
	}

	remotes := make([]cluster.RemoteNode, 0, len(consenters))
	for _, co := range consenters {
		node, err := consenterToRemoteNode(co, msps)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "bdls cluster wiring: consenter id=%d", co.Id)
		}
		if logger != nil {
			logger.Debugf("bdls cluster wiring: remote node id=%d endpoint=%s:%d msp=%s",
				co.Id, co.Host, co.Port, co.MspId)
		}
		remotes = append(remotes, node)
	}
	return remotes, consenters, nil
}

// consenterToRemoteNode converts a single channel-config Consenter into a
// cluster.RemoteNode, pulling its MSP's TLS root CAs so cluster.AuthCommMgr
// can validate the peer's server cert at dial time.
func consenterToRemoteNode(co *cb.Consenter, msps map[string]msp.MSP) (cluster.RemoteNode, error) {
	serverDER, err := pemCertToDER(co.ServerTlsCert)
	if err != nil {
		return cluster.RemoteNode{}, errors.Wrap(err, "server TLS cert")
	}
	clientDER, err := pemCertToDER(co.ClientTlsCert)
	if err != nil {
		return cluster.RemoteNode{}, errors.Wrap(err, "client TLS cert")
	}
	nodeMSP, ok := msps[co.MspId]
	if !ok {
		return cluster.RemoteNode{}, errors.Errorf("no MSP registered for id %q", co.MspId)
	}
	var rootCAs [][]byte
	rootCAs = append(rootCAs, nodeMSP.GetTLSRootCerts()...)
	rootCAs = append(rootCAs, nodeMSP.GetTLSIntermediateCerts()...)

	sanitizedIdentity, err := crypto.SanitizeX509Cert(co.Identity)
	if err != nil {
		return cluster.RemoteNode{}, errors.Wrap(err, "sanitising identity")
	}

	return cluster.RemoteNode{
		NodeAddress: cluster.NodeAddress{
			ID:       uint64(co.Id),
			Endpoint: fmt.Sprintf("%s:%d", co.Host, co.Port),
		},
		NodeCerts: cluster.NodeCerts{
			ServerTLSCert: serverDER,
			ClientTLSCert: clientDER,
			ServerRootCA:  rootCAs,
			Identity:      sanitizedIdentity,
		},
	}, nil
}

// pemCertToDER decodes a single PEM CERTIFICATE block to its DER bytes. BDLS
// stores TLS certs as PEM in ConfigMetadata (same as smartbft); cluster.
// RemoteNode wants DER; the conversion is a pem.Decode away.
func pemCertToDER(pemBytes []byte) ([]byte, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("input is not a PEM block")
	}
	return block.Bytes, nil
}

// blockRetrieverFromSupport adapts a ConsenterSupport into the tiny interface
// cluster.LastConfigBlock expects. We only need Block(uint64), and
// ConsenterSupport already provides it, so this adapter is a one-liner.
type blockRetrieverFromSupport struct {
	support consensus.ConsenterSupport
}

func (b blockRetrieverFromSupport) Block(number uint64) *cb.Block {
	return b.support.Block(number)
}

// compile-time guarantee the helper remains in sync with cluster.BlockRetriever.
var _ cluster.BlockRetriever = blockRetrieverFromSupport{}
