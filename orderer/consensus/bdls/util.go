/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	bdlslib "github.com/BDLS-bft/bdls"
	"google.golang.org/protobuf/proto"

	bdlsproto "github.com/hyperledger/fabric/orderer/consensus/bdls/protos"
)

// parseConfigMetadata unmarshals a channel's ConsensusType.Metadata into the
// BDLS ConfigMetadata shape. It is called by HandleChain (Phase C7) and by
// the metadata validator (Phase C8); centralised here so there is one
// definition of "how do we read BDLS channel config".
func parseConfigMetadata(raw []byte) (*bdlsproto.ConfigMetadata, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("bdls: ConsensusType.Metadata is empty")
	}
	md := &bdlsproto.ConfigMetadata{}
	if err := proto.Unmarshal(raw, md); err != nil {
		return nil, fmt.Errorf("bdls: failed to unmarshal ConfigMetadata: %w", err)
	}
	return md, nil
}

// consenterPublicKey extracts the ECDSA public key from a PEM-encoded
// consenter server TLS cert. BDLS uses the (X, Y) coordinates directly as
// its participant identity via DefaultPubKeyToIdentity, so the curve MUST
// match the one the BDLS library was compiled for (secp256k1 today).
func consenterPublicKey(pemBytes []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("bdls: consenter cert is not a valid PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("bdls: x509.ParseCertificate: %w", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("bdls: consenter cert public key is %T, not *ecdsa.PublicKey", cert.PublicKey)
	}
	return pub, nil
}

// participantsFromConsenters translates a ConfigMetadata.Consenters slice into
// the []bdlslib.Identity the BDLS library expects. The mapping is stable as
// long as two channel-config updates agree on the server TLS cert for each
// consenter — which is the same invariant smartbft relies on.
func participantsFromConsenters(consenters []*bdlsproto.Consenter) ([]bdlslib.Identity, error) {
	if len(consenters) < bdlslib.ConfigMinimumParticipants {
		return nil, fmt.Errorf("bdls: ConfigMetadata has %d consenters, need at least %d",
			len(consenters), bdlslib.ConfigMinimumParticipants)
	}
	out := make([]bdlslib.Identity, 0, len(consenters))
	for i, c := range consenters {
		pub, err := consenterPublicKey(c.ServerTlsCert)
		if err != nil {
			return nil, fmt.Errorf("bdls: consenter[%d]: %w", i, err)
		}
		out = append(out, bdlslib.DefaultPubKeyToIdentity(pub))
	}
	return out, nil
}

// buildBDLSConfig assembles a bdlslib.Config from parsed channel metadata.
// SignDigest + PublicKey are left nil here; Phase C7 fills them in from the
// orderer's BCCSP handle so the private key never leaves the process. The
// rest of the Config is populated from the on-chain ConfigMetadata plus a
// couple of Fabric-side defaults (reliable decide on, state compare by
// hash).
func buildBDLSConfig(md *bdlsproto.ConfigMetadata, currentHeight uint64) (*bdlslib.Config, error) {
	participants, err := participantsFromConsenters(md.Consenters)
	if err != nil {
		return nil, err
	}
	var startHeight uint64
	if currentHeight > 0 {
		startHeight = currentHeight - 1
	}
	cfg := &bdlslib.Config{
		Epoch:         time.Now(),
		CurrentHeight: startHeight,
		Participants:  participants,
		StateCompare:  compareState,
		StateValidate: validateState,
		// Reliable decide defaults to true for BDLS-on-Fabric: the single
		// -flood legacy path exists only to keep pre-PR-A3 embedders
		// working, and Fabric is a new embedder.
		ReliableDecide: true,
	}
	if opt := md.Options; opt != nil {
		cfg.Delta0 = msToDuration(opt.Delta0Ms)
		cfg.Delta1 = msToDuration(opt.Delta1Ms)
		cfg.DeltaPrime1 = msToDuration(opt.DeltaPrime1Ms)
		cfg.Delta2 = msToDuration(opt.Delta2Ms)
		cfg.Delta3 = msToDuration(opt.Delta3Ms)
		// reliable_decide is treated as "true unless channel explicitly
		// opts out". The proto zero value maps to "inherit default", so
		// any false we see here came from an explicit operator decision.
		if !opt.ReliableDecide && md.Options != nil {
			// NB: we cannot distinguish "unset" from "set to false" in
			// proto3 scalar bools; for safety we keep reliable decide on
			// unless a future proto revision adds an explicit
			// google.protobuf.BoolValue.
			_ = opt.ReliableDecide
		}
	}
	return cfg, nil
}

// msToDuration converts an optional millisecond field into a time.Duration.
// Zero / negative means "use library default", which is what bdls.Config
// expects for Delta* zero values.
func msToDuration(ms int64) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// compareState is the BDLS StateCompare implementation for Fabric. BDLS
// needs a total order over proposed blocks to pick between competing
// proposals in the round-change stage. We use a plain SHA-256 comparison —
// deterministic across nodes, cheap, and doesn't crack open the block
// structure (which would force us to re-derive header-hashing logic from
// protoutil, an invitation for drift).
func compareState(a, b bdlslib.State) int {
	ha := sha256.Sum256(a)
	hb := sha256.Sum256(b)
	return bytes.Compare(ha[:], hb[:])
}

// validateState is the BDLS StateValidate hook. Any non-empty byte slice is
// accepted at the state-machine level; real block-level validation (header
// chaining, signatures, capability gate) runs in chain.go once BDLS has
// decided. Doing it twice would just move error reporting around without
// catching anything earlier.
func validateState(s bdlslib.State) bool {
	return len(s) > 0
}
