/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	//"bytes"
	//"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"sort"

	//"time"

	//"github.com/hyperledger-labs/SmartBFT/pkg/types"
	//"github.com/hyperledger-labs/SmartBFT/smartbftprotos"
	//"github.com/hyperledger/fabric-lib-go/bccsp"
	"github.com/hyperledger/fabric-lib-go/bccsp"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"

	//"github.com/hyperledger/fabric-protos-go-apiv2/orderer/smartbft"
	"github.com/hyperledger/fabric/common/channelconfig"
	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/common/deliverclient"

	//"github.com/hyperledger/fabric/common/deliverclient/blocksprovider"
	//"github.com/hyperledger/fabric/internal/pkg/identity"
	"github.com/hyperledger/fabric/orderer/common/cluster"
	//"github.com/BDLS-bft/bdls"
	//"github.com/hyperledger/fabric/orderer/consensus"
	//"github.com/hyperledger/fabric/orderer/consensus/smartbft/util"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"
)

// RuntimeConfig defines the configuration of the consensus
// that is related to runtime.
type RuntimeConfig struct {
	selfID                 uint64
	isConfig               bool
	logger                 *flogging.FabricLogger
	id                     uint64
	LastCommittedBlockHash string
	RemoteNodes            []cluster.RemoteNode
	ID2Identities          NodeIdentitiesByID
	LastBlock              *cb.Block
	LastConfigBlock        *cb.Block
	Nodes                  []uint64
	consenters             []*cb.Consenter
}

type nodeConfig struct {
	id2Identities NodeIdentitiesByID
	remoteNodes   []cluster.RemoteNode
	nodeIDs       []uint64
	consenters    []*cb.Consenter
}

type NodeIdentitiesByID map[uint64][]byte

// IdentityToID looks up the Identity in NodeIdentitiesByID and returns id and flag true if found
func (nibd NodeIdentitiesByID) IdentityToID(identity []byte) (uint64, bool) {
	sID := &msp.SerializedIdentity{}
	if err := proto.Unmarshal(identity, sID); err != nil {
		return 0, false
	}
	for id, currIdentity := range nibd {
		currentID := &msp.SerializedIdentity{}
		if err := proto.Unmarshal(currIdentity, currentID); err != nil {
			return 0, false
		}
		if proto.Equal(currentID, sID) {
			return id, true
		}
	}
	return 0, false
}

// BlockCommitted updates the config from the block
func (rtc RuntimeConfig) BlockCommitted(block *cb.Block, bccsp bccsp.BCCSP) (RuntimeConfig, error) {
	if _, err := deliverclient.ConfigFromBlock(block); err == nil {
		return rtc.configBlockCommitted(block, bccsp)
	}
	return RuntimeConfig{
		consenters: rtc.consenters,
		//BFTConfig:              rtc.BFTConfig,
		selfID:                 rtc.selfID,
		id:                     rtc.id,
		logger:                 rtc.logger,
		LastCommittedBlockHash: hex.EncodeToString(protoutil.BlockHeaderHash(block.Header)),
		Nodes:                  rtc.Nodes,
		ID2Identities:          rtc.ID2Identities,
		RemoteNodes:            rtc.RemoteNodes,
		LastBlock:              block,
		LastConfigBlock:        rtc.LastConfigBlock,
	}, nil
}

func (rtc RuntimeConfig) configBlockCommitted(block *cb.Block, bccsp bccsp.BCCSP) (RuntimeConfig, error) {
	nodeConf, err := RemoteNodesFromConfigBlock(block, rtc.logger, bccsp)
	if err != nil {
		return rtc, errors.Wrap(err, "remote nodes cannot be computed, rejecting config block")
	}

	return RuntimeConfig{
		consenters:             nodeConf.consenters,
		selfID:                 rtc.selfID,
		isConfig:               true,
		id:                     rtc.id,
		logger:                 rtc.logger,
		LastCommittedBlockHash: hex.EncodeToString(protoutil.BlockHeaderHash(block.Header)),
		Nodes:                  nodeConf.nodeIDs,
		ID2Identities:          nodeConf.id2Identities,
		RemoteNodes:            nodeConf.remoteNodes,
		LastBlock:              block,
		LastConfigBlock:        block,
	}, nil
}

// RemoteNodesFromConfigBlock unmarshals the node config from the block metadata
func RemoteNodesFromConfigBlock(block *cb.Block, logger *flogging.FabricLogger, bccsp bccsp.BCCSP) (*nodeConfig, error) {
	env := &cb.Envelope{}
	if err := proto.Unmarshal(block.Data.Data[0], env); err != nil {
		return nil, errors.Wrap(err, "failed unmarshaling envelope of config block")
	}
	bundle, err := channelconfig.NewBundleFromEnvelope(env, bccsp)
	if err != nil {
		return nil, errors.Wrap(err, "failed getting a new bundle from envelope of config block")
	}

	channelMSPs, err := bundle.MSPManager().GetMSPs()
	if err != nil {
		return nil, errors.Wrap(err, "failed obtaining MSPs from MSPManager")
	}

	oc, ok := bundle.OrdererConfig()
	if !ok {
		return nil, errors.New("no orderer config in config block")
	}

	// _, err = createSmartBftConfig(oc)
	// if err != nil {
	// 	return nil, err
	// }

	var nodeIDs []uint64
	var remoteNodes []cluster.RemoteNode
	id2Identies := map[uint64][]byte{}
	for _, consenter := range oc.Consenters() {
		sanitizedID, err := crypto.SanitizeIdentity(protoutil.MarshalOrPanic(&msp.SerializedIdentity{
			IdBytes: consenter.Identity,
			Mspid:   consenter.MspId,
		}))
		if err != nil {
			logger.Panicf("Failed to sanitize identity: %v [%s]", err, string(consenter.Identity))
		}
		id2Identies[(uint64)(consenter.Id)] = sanitizedID
		logger.Infof("%s %d ---> %s", bundle.ConfigtxValidator().ChannelID(), consenter.Id, string(consenter.Identity))

		nodeIDs = append(nodeIDs, (uint64)(consenter.Id))

		serverCertAsDER, err := pemToDER(consenter.ServerTlsCert, (uint64)(consenter.Id), "server", logger)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		clientCertAsDER, err := pemToDER(consenter.ClientTlsCert, (uint64)(consenter.Id), "client", logger)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		// Validate certificate structure
		for _, cert := range [][]byte{serverCertAsDER, clientCertAsDER} {
			if _, err := x509.ParseCertificate(cert); err != nil {
				pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})
				logger.Errorf("Invalid certificate: %s", string(pemBytes))
				return nil, err
			}
		}

		nodeMSP, exists := channelMSPs[consenter.MspId]
		if !exists {
			return nil, errors.Errorf("no MSP found for MSP with ID of %s", consenter.MspId)
		}

		var rootCAs [][]byte
		rootCAs = append(rootCAs, nodeMSP.GetTLSRootCerts()...)
		rootCAs = append(rootCAs, nodeMSP.GetTLSIntermediateCerts()...)

		sanitizedCert, err := crypto.SanitizeX509Cert(consenter.Identity)
		if err != nil {
			return nil, err
		}

		remoteNodes = append(remoteNodes, cluster.RemoteNode{
			NodeAddress: cluster.NodeAddress{
				ID:       (uint64)(consenter.Id),
				Endpoint: fmt.Sprintf("%s:%d", consenter.Host, consenter.Port),
			},

			NodeCerts: cluster.NodeCerts{
				ClientTLSCert: clientCertAsDER,
				ServerTLSCert: serverCertAsDER,
				ServerRootCA:  rootCAs,
				Identity:      sanitizedCert,
			},
		})
	}

	sort.Slice(nodeIDs, func(i, j int) bool {
		return nodeIDs[i] < nodeIDs[j]
	})

	return &nodeConfig{
		consenters:    oc.Consenters(),
		remoteNodes:   remoteNodes,
		id2Identities: id2Identies,
		nodeIDs:       nodeIDs,
	}, nil
}

func pemToDER(pemBytes []byte, id uint64, certType string, logger *flogging.FabricLogger) ([]byte, error) {
	bl, _ := pem.Decode(pemBytes)
	if bl == nil {
		logger.Errorf("Rejecting PEM block of %s TLS cert for node %d, offending PEM is: %s", certType, id, string(pemBytes))
		return nil, errors.Errorf("invalid PEM block")
	}
	return bl.Bytes, nil
}
