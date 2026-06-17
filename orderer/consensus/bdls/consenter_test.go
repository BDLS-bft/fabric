/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	bdlsproto "github.com/hyperledger/fabric/orderer/consensus/bdls/protos"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// detectSelfID
// ---------------------------------------------------------------------------

func TestDetectSelfID_HappyPath(t *testing.T) {
	consenters, keys := makeProtoConsenters(t, 4)
	// Pick consenter[2] as "us".
	c := &Consenter{
		Logger:       flogging.MustGetLogger("test"),
		TLSPublicKey: &keys[2].PublicKey,
	}
	md := &bdlsproto.ConfigMetadata{Consenters: consenters}
	id, err := c.detectSelfID(md)
	require.NoError(t, err)
	require.EqualValues(t, 2, id)
}

func TestDetectSelfID_NotFound(t *testing.T) {
	consenters, _ := makeProtoConsenters(t, 4)
	stranger, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	c := &Consenter{
		Logger:       flogging.MustGetLogger("test"),
		TLSPublicKey: &stranger.PublicKey,
	}
	md := &bdlsproto.ConfigMetadata{Consenters: consenters}
	_, err = c.detectSelfID(md)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in the channel")
}

func TestDetectSelfID_NilTLSPublicKey(t *testing.T) {
	c := &Consenter{Logger: flogging.MustGetLogger("test")}
	md := &bdlsproto.ConfigMetadata{}
	_, err := c.detectSelfID(md)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no cluster TLS public key")
}

func TestDetectSelfID_BadCertSkipped(t *testing.T) {
	consenters, keys := makeProtoConsenters(t, 4)
	// Corrupt consenter[1]'s cert. detectSelfID should log a warning
	// but keep going and find us at index 2.
	consenters[1].ServerTlsCert = []byte("garbage")
	c := &Consenter{
		Logger:       flogging.MustGetLogger("test"),
		TLSPublicKey: &keys[2].PublicKey,
	}
	md := &bdlsproto.ConfigMetadata{Consenters: consenters}
	id, err := c.detectSelfID(md)
	require.NoError(t, err)
	require.EqualValues(t, 2, id)
}

// ---------------------------------------------------------------------------
// HandleChain: only test the guard clauses that don't require a full
// ConsenterSupport mock.
// ---------------------------------------------------------------------------

func TestHandleChain_NoTLSKey(t *testing.T) {
	c := &Consenter{
		Logger: flogging.MustGetLogger("test"),
	}
	_, err := c.HandleChain(nil, nil)
	require.ErrorIs(t, err, ErrClusterTLSKeyUnavailable)
}

func TestHandleChain_NoClusterComm(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	c := &Consenter{
		Logger:        flogging.MustGetLogger("test"),
		TLSPrivateKey: priv,
		TLSPublicKey:  &priv.PublicKey,
	}
	_, err = c.HandleChain(nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Comm/ClusterService not wired")
}
