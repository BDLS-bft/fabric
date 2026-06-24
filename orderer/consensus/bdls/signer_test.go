/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// loadClusterTLSPrivateKey
// ---------------------------------------------------------------------------

func writePEMFile(t *testing.T, name string, blockType string, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	buf := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	require.NoError(t, os.WriteFile(path, buf, 0o600))
	return path
}

func TestLoadClusterTLSPrivateKey_PKCS8(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)

	path := writePEMFile(t, "key.pem", "PRIVATE KEY", der)
	got, err := loadClusterTLSPrivateKey(path)
	require.NoError(t, err)
	require.True(t, priv.Equal(got))
}

func TestLoadClusterTLSPrivateKey_SEC1(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)

	path := writePEMFile(t, "key.pem", "EC PRIVATE KEY", der)
	got, err := loadClusterTLSPrivateKey(path)
	require.NoError(t, err)
	require.True(t, priv.Equal(got))
}

func TestLoadClusterTLSPrivateKey_EmptyPath(t *testing.T) {
	_, err := loadClusterTLSPrivateKey("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ClientPrivateKey is unset")
}

func TestLoadClusterTLSPrivateKey_MissingFile(t *testing.T) {
	_, err := loadClusterTLSPrivateKey(filepath.Join(t.TempDir(), "nope.pem"))
	require.Error(t, err)
}

func TestLoadClusterTLSPrivateKey_BadPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	require.NoError(t, os.WriteFile(path, []byte("not a pem"), 0o600))
	_, err := loadClusterTLSPrivateKey(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a valid PEM block")
}

func TestLoadClusterTLSPrivateKey_NotECDSA(t *testing.T) {
	// An RSA key in PKCS8 form should be rejected with a clear message.
	// We synthesize the smallest thing that will parse as PKCS8: an
	// Ed25519 key (tiny, no big-number machinery).
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(edPriv)
	require.NoError(t, err)
	path := writePEMFile(t, "ed.pem", "PRIVATE KEY", der)

	_, err = loadClusterTLSPrivateKey(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "BDLS requires an ECDSA key")
}

// ---------------------------------------------------------------------------
// publicKeyFromTLSCert / publicKeysEqual
// ---------------------------------------------------------------------------

func TestPublicKeyFromTLSCert_HappyPath(t *testing.T) {
	cert, priv := genTestECDSACert(t)
	pub, err := publicKeyFromTLSCert(cert)
	require.NoError(t, err)
	require.True(t, publicKeysEqual(pub, &priv.PublicKey))
}

func TestPublicKeyFromTLSCert_BadPEM(t *testing.T) {
	_, err := publicKeyFromTLSCert([]byte("nope"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not valid PEM")
}

func TestPublicKeyFromTLSCert_BadCert(t *testing.T) {
	// Valid PEM wrapper around junk DER.
	junk := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{0x00, 0x01, 0x02}})
	_, err := publicKeyFromTLSCert(junk)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing consenter ServerTlsCert")
}

func TestPublicKeysEqual(t *testing.T) {
	a, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	b, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	require.True(t, publicKeysEqual(&a.PublicKey, &a.PublicKey))
	require.False(t, publicKeysEqual(&a.PublicKey, &b.PublicKey))
	require.False(t, publicKeysEqual(nil, &a.PublicKey))
	require.False(t, publicKeysEqual(&a.PublicKey, nil))

	// Different curves must never compare equal.
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	require.False(t, publicKeysEqual(&a.PublicKey, &p384.PublicKey))
}
