/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	bdlslib "github.com/BDLS-bft/bdls"
	bdlsproto "github.com/hyperledger/fabric/orderer/consensus/bdls/protos"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// genTestECDSACert generates a minimal self-signed ECDSA P-256 certificate
// suitable for the server-TLS-cert fields the BDLS config-metadata proto
// expects. The private key is returned so tests can exercise signer helpers
// against the same identity.
func genTestECDSACert(t *testing.T) (pemBytes []byte, priv *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bdls-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)

	pemBytes = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return pemBytes, priv
}

// makeProtoConsenters builds n BDLS proto Consenters with unique test certs.
// Each entry's ServerTlsCert is a valid self-signed P-256 cert; Host/Port are
// placeholders that the util functions don't look at.
func makeProtoConsenters(t *testing.T, n int) ([]*bdlsproto.Consenter, []*ecdsa.PrivateKey) {
	t.Helper()
	out := make([]*bdlsproto.Consenter, 0, n)
	keys := make([]*ecdsa.PrivateKey, 0, n)
	for i := 0; i < n; i++ {
		cert, priv := genTestECDSACert(t)
		out = append(out, &bdlsproto.Consenter{
			Host:          "127.0.0.1",
			Port:          uint32(7050 + i),
			ServerTlsCert: cert,
		})
		keys = append(keys, priv)
	}
	return out, keys
}

// ---------------------------------------------------------------------------
// parseConfigMetadata
// ---------------------------------------------------------------------------

func TestParseConfigMetadata_HappyPath(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, 4)
	in := &bdlsproto.ConfigMetadata{
		Consenters: consenters,
		Options: &bdlsproto.Options{
			Delta0Ms:       100,
			Delta1Ms:       200,
			ReliableDecide: true,
		},
	}
	raw, err := proto.Marshal(in)
	require.NoError(t, err)

	got, err := parseConfigMetadata(raw)
	require.NoError(t, err)
	require.Len(t, got.Consenters, 4)
	require.NotNil(t, got.Options)
	require.EqualValues(t, 100, got.Options.Delta0Ms)
	require.True(t, got.Options.ReliableDecide)
}

func TestParseConfigMetadata_Empty(t *testing.T) {
	_, err := parseConfigMetadata(nil)
	require.Error(t, err)
	_, err = parseConfigMetadata([]byte{})
	require.Error(t, err)
}

func TestParseConfigMetadata_Garbage(t *testing.T) {
	_, err := parseConfigMetadata([]byte{0xff, 0xff, 0xff, 0xff})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// participantsFromConsenters
// ---------------------------------------------------------------------------

func TestParticipantsFromConsenters_HappyPath(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, bdlslib.ConfigMinimumParticipants)
	ids, err := participantsFromConsenters(consenters)
	require.NoError(t, err)
	require.Len(t, ids, bdlslib.ConfigMinimumParticipants)

	// Identities must all be distinct — DefaultPubKeyToIdentity hashes
	// (X, Y) so two different ECDSA keys can't collide.
	seen := make(map[bdlslib.Identity]struct{})
	for _, id := range ids {
		_, dup := seen[id]
		require.False(t, dup, "duplicate identity %x", id)
		seen[id] = struct{}{}
	}
}

func TestParticipantsFromConsenters_TooFew(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, bdlslib.ConfigMinimumParticipants-1)
	_, err := participantsFromConsenters(consenters)
	require.Error(t, err)
	require.Contains(t, err.Error(), "need at least")
}

func TestParticipantsFromConsenters_BadPEM(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, bdlslib.ConfigMinimumParticipants)
	consenters[0].ServerTlsCert = []byte("not a pem block")
	_, err := participantsFromConsenters(consenters)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// buildBDLSConfig
// ---------------------------------------------------------------------------

func TestBuildBDLSConfig_PropagatesOptions(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, 4)
	md := &bdlsproto.ConfigMetadata{
		Consenters: consenters,
		Options: &bdlsproto.Options{
			Delta0Ms:      50,
			Delta1Ms:      60,
			DeltaPrime1Ms: 70,
			Delta2Ms:      80,
			Delta3Ms:      90,
		},
	}
	cfg, err := buildBDLSConfig(md, 5)
	require.NoError(t, err)
	require.Len(t, cfg.Participants, 4)
	require.EqualValues(t, 4, cfg.CurrentHeight)
	require.Equal(t, 50*time.Millisecond, cfg.Delta0)
	require.Equal(t, 60*time.Millisecond, cfg.Delta1)
	require.Equal(t, 70*time.Millisecond, cfg.DeltaPrime1)
	require.Equal(t, 80*time.Millisecond, cfg.Delta2)
	require.Equal(t, 90*time.Millisecond, cfg.Delta3)
	require.True(t, cfg.ReliableDecide, "ReliableDecide must default on")
	require.NotNil(t, cfg.StateCompare)
	require.NotNil(t, cfg.StateValidate)
}

func TestBuildBDLSConfig_NilOptionsOK(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, 4)
	md := &bdlsproto.ConfigMetadata{Consenters: consenters}
	cfg, err := buildBDLSConfig(md, 0)
	require.NoError(t, err)
	require.Zero(t, cfg.Delta0, "missing options must leave Δ at library default")
	require.True(t, cfg.ReliableDecide)
}

func TestBuildBDLSConfig_TooFewConsenters(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, 1)
	md := &bdlsproto.ConfigMetadata{Consenters: consenters}
	_, err := buildBDLSConfig(md, 0)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// compareState / validateState / msToDuration
// ---------------------------------------------------------------------------

func TestCompareState_Deterministic(t *testing.T) {
	a := []byte("hello")
	b := []byte("world")
	require.Equal(t, 0, compareState(a, a))
	// SHA-256(hello) < SHA-256(world) → helper returns negative.
	cmp := compareState(a, b)
	require.NotEqual(t, 0, cmp)
	// symmetry
	require.Equal(t, -cmp, compareState(b, a))
}

func TestValidateState(t *testing.T) {
	require.True(t, validateState([]byte{0x01}))
	require.False(t, validateState(nil))
	require.False(t, validateState([]byte{}))
}

func TestMsToDuration(t *testing.T) {
	require.Equal(t, time.Duration(0), msToDuration(0))
	require.Equal(t, time.Duration(0), msToDuration(-5))
	require.Equal(t, 250*time.Millisecond, msToDuration(250))
}
