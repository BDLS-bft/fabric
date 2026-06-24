/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"

	"github.com/pkg/errors"
)

// ---------------------------------------------------------------------------
// signer.go owns the "how do we sign a BDLS digest" story.
//
// BDLS identifies each participant by the (X, Y) coordinates of an ECDSA
// public key (see DefaultPubKeyToIdentity in the upstream library). For
// Fabric, we elected to reuse the orderer's *cluster TLS* keypair as the
// BDLS identity rather than invent a new one:
//
//   * The public half is already carried in the channel's ConfigMetadata
//     as each consenter's ServerTlsCert — no extra YAML surface to
//     configure, no extra path for a key-to-identity mismatch to hide in.
//
//   * The private half lives at conf.General.Cluster.ClientPrivateKey on
//     disk, which is an operator-visible PEM file. Loading it from there
//     avoids reaching into msp.SigningIdentity internals to dig out a
//     bccsp.Key handle — that path was what made Phase C7's original
//     scope note call this "invasive enough to deserve its own review".
//
//   * ECDSA P-256 is what Fabric's default BCCSP and what secp256k1 /
//     P-256 BDLS compile against, so there is no curve mismatch to
//     handle: if the key parses as ECDSA, it is usable as-is.
//
// The trade-off this makes: the TLS session key doubles as the BDLS
// signing key. An attacker who compromises the TLS private key gets
// forging-power over BDLS <propose>/<lock>/<decide> messages as well,
// whereas with a separate BDLS key they would only get the ability to
// impersonate the cluster endpoint. Given that a peer who can
// impersonate the cluster endpoint can already intercept and replay
// BDLS traffic inside the mTLS session, we consider the two powers
// effectively equivalent and the ergonomics win of "one key, one
// config block entry" outweighs the theoretical separation. This is
// the same reasoning Fabric applies to smartbft's block-level
// signatures using the cluster TLS identity for BFT message signing.
// ---------------------------------------------------------------------------

// loadClusterTLSPrivateKey reads and parses the orderer's cluster TLS
// private key from its on-disk PEM file. The returned key must be ECDSA;
// BDLS has no support for RSA or Ed25519 participants and we do not try
// to coerce.
//
// Called once at bdls.New() time. Failure here is fatal for the BDLS
// consenter's ability to HandleChain a BDLS channel, but does not
// affect etcdraft/smartbft channels on the same orderer — main.go
// surfaces the error as a warning and the consenter stays registered
// in a "declines all chains" state so operators get a clear error
// message from the Registrar rather than a crash loop.
func loadClusterTLSPrivateKey(keyPath string) (*ecdsa.PrivateKey, error) {
	if keyPath == "" {
		return nil, errors.New("bdls signer: General.Cluster.ClientPrivateKey is unset; set it to the PEM file whose public key appears in each channel's ConfigMetadata.Consenters[*].ServerTlsCert")
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, errors.Wrapf(err, "bdls signer: reading cluster TLS private key %q", keyPath)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.Errorf("bdls signer: %q is not a valid PEM block", keyPath)
	}

	// Fabric ships cluster keys as PKCS#8 by default but older deployments
	// still use the SEC 1 / "EC PRIVATE KEY" form — try both before
	// giving up. The order matches openssl's default output priority.
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.Errorf("bdls signer: %q parses as %T, BDLS requires an ECDSA key", keyPath, key)
		}
		return ec, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.Errorf("bdls signer: %q is not a PKCS#8 or SEC1 ECDSA private key", keyPath)
}

// publicKeyFromTLSCert extracts an *ecdsa.PublicKey from a PEM-encoded
// cluster TLS certificate. It is the cert-side counterpart of
// loadClusterTLSPrivateKey: during detectSelfID we walk
// ConfigMetadata.Consenters[*].ServerTlsCert and match each entry's
// public key against the one derived from our own cluster TLS private
// key, so this helper does the cert → *ecdsa.PublicKey hop once per
// consenter.
//
// Keeping it here (next to loadClusterTLSPrivateKey) rather than in
// util.go means reviewers can confirm the "we use TLS cert pubkeys for
// participant identity" story from a single file.
func publicKeyFromTLSCert(pemBytes []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("bdls signer: consenter ServerTlsCert is not valid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "bdls signer: parsing consenter ServerTlsCert")
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.Errorf("bdls signer: consenter ServerTlsCert public key is %T, not *ecdsa.PublicKey", cert.PublicKey)
	}
	return pub, nil
}

// publicKeysEqual compares two ECDSA public keys by curve + (X, Y). The
// stdlib does not provide an Equal method on *ecdsa.PublicKey in older
// Go versions, so we spell out the comparison to keep the code
// buildable on whatever toolchain Fabric's go.mod pins.
func publicKeysEqual(a, b *ecdsa.PublicKey) bool {
	if a == nil || b == nil {
		return false
	}
	// Curve identity is a pointer comparison for the stdlib named
	// curves (elliptic.P256() etc. return singletons), which is what
	// we want — two keys on different curves are not interchangeable
	// regardless of coordinate equality.
	if a.Curve != b.Curve {
		// Accept two references to the same named curve that happen
		// to have been constructed separately: Params().Name match is
		// the universally-portable fallback.
		ac, bc := curveName(a.Curve), curveName(b.Curve)
		if ac == "" || ac != bc {
			return false
		}
	}
	return bigIntEqual(a.X, b.X) && bigIntEqual(a.Y, b.Y)
}

func curveName(c elliptic.Curve) string {
	if c == nil || c.Params() == nil {
		return ""
	}
	return c.Params().Name
}

func bigIntEqual(a, b *big.Int) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Cmp(b) == 0
}
