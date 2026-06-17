/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"testing"
	"time"

	bdlslib "github.com/BDLS-bft/bdls"
	ab "github.com/hyperledger/fabric-protos-go-apiv2/orderer"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	bdlsproto "github.com/hyperledger/fabric/orderer/consensus/bdls/protos"

	"github.com/hyperledger/fabric/common/channelconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// stubOrdererConfig implements channelconfig.Orderer for the metadata
// validator tests. Only the three fields the validator inspects are
// populated; all others return zero values.
// ---------------------------------------------------------------------------

type stubOrdererConfig struct {
	cType    string
	metadata []byte
}

func (s *stubOrdererConfig) ConsensusType() string     { return s.cType }
func (s *stubOrdererConfig) ConsensusMetadata() []byte { return s.metadata }
func (s *stubOrdererConfig) ConsensusState() ab.ConsensusType_State {
	return ab.ConsensusType_STATE_NORMAL
}

func (s *stubOrdererConfig) BatchSize() *ab.BatchSize                           { return &ab.BatchSize{} }
func (s *stubOrdererConfig) BatchTimeout() time.Duration                        { return 0 }
func (s *stubOrdererConfig) MaxChannelsCount() uint64                           { return 0 }
func (s *stubOrdererConfig) Consenters() []*cb.Consenter                        { return nil }
func (s *stubOrdererConfig) Organizations() map[string]channelconfig.OrdererOrg { return nil }
func (s *stubOrdererConfig) Capabilities() channelconfig.OrdererCapabilities    { return nil }

// helper — marshal a ConfigMetadata into a stubOrdererConfig.
func stubOCFromMD(t *testing.T, md *bdlsproto.ConfigMetadata) *stubOrdererConfig {
	t.Helper()
	raw, err := proto.Marshal(md)
	require.NoError(t, err)
	return &stubOrdererConfig{cType: "BDLS", metadata: raw}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMetadataValidator_NewChannel_HappyPath(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, bdlslib.ConfigMinimumParticipants)
	md := &bdlsproto.ConfigMetadata{
		Consenters: consenters,
		Options:    &bdlsproto.Options{Delta0Ms: 100},
	}
	oc := stubOCFromMD(t, md)

	var c *Chain // nil chain is fine for new-channel path
	err := c.ValidateConsensusMetadata(nil, oc, true)
	require.NoError(t, err)
}

func TestMetadataValidator_NilNewConfig(t *testing.T) {
	var c *Chain
	err := c.ValidateConsensusMetadata(nil, nil, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil new channel config")
}

func TestMetadataValidator_NilMetadata_OK(t *testing.T) {
	// A config update that doesn't touch consensus metadata should pass.
	oc := &stubOrdererConfig{cType: "BDLS", metadata: nil}
	var c *Chain
	err := c.ValidateConsensusMetadata(nil, oc, false)
	require.NoError(t, err)
}

func TestMetadataValidator_NonBDLS_Passthrough(t *testing.T) {
	oc := &stubOrdererConfig{cType: "etcdraft", metadata: []byte("whatever")}
	var c *Chain
	err := c.ValidateConsensusMetadata(nil, oc, false)
	require.NoError(t, err)
}

func TestMetadataValidator_TooFewConsenters(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, 1)
	md := &bdlsproto.ConfigMetadata{Consenters: consenters}
	oc := stubOCFromMD(t, md)

	var c *Chain
	err := c.ValidateConsensusMetadata(nil, oc, true)
	require.Error(t, err)
}

func TestMetadataValidator_NegativeDelta(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, bdlslib.ConfigMinimumParticipants)
	md := &bdlsproto.ConfigMetadata{
		Consenters: consenters,
		Options:    &bdlsproto.Options{Delta0Ms: -5},
	}
	oc := stubOCFromMD(t, md)

	var c *Chain
	err := c.ValidateConsensusMetadata(nil, oc, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "negative")
}

func TestMetadataValidator_GarbageMetadata(t *testing.T) {
	oc := &stubOrdererConfig{cType: "BDLS", metadata: []byte{0xff, 0xff}}
	var c *Chain
	err := c.ValidateConsensusMetadata(nil, oc, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing new ConfigMetadata")
}
