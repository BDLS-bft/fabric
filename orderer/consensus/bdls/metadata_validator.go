/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	bdlslib "github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric/common/channelconfig"
	"github.com/pkg/errors"
)

// ---------------------------------------------------------------------------
// metadata_validator.go implements consensus.MetadataValidator for BDLS.
// The Registrar discovers this method on *Chain via a type assertion in
// orderer/common/multichannel/chainsupport.go — if we don't provide one it
// falls back to a no-op validator, which is what smartbft does today.
//
// We DO want a real validator for BDLS because:
//
//   * ConfigMetadata.Options carries all five BDLS Δ knobs plus
//     ReliableDecide — a config update that ships with, say, a negative
//     Delta0 or a two-consenter set would be silently accepted by a no-op
//     validator and then crash HandleChain on the next chain rebuild. We
//     want the config transaction to be rejected up front instead.
//
//   * BDLS consenter membership changes must preserve a 3f+1 quorum. A
//     validator that runs before the config block commits is the last
//     opportunity to say "this change would wedge the channel" while the
//     channel is still ordering.
//
// What we do NOT do here:
//
//   * We do not attempt a "migration from BFT/etcdraft → BDLS" check. The
//     plan treats consenter migration as an operator-driven reset (new
//     channel, new genesis block) rather than an in-place transition,
//     because BDLS uses an entirely different participant-id scheme (ECDSA
//     (X,Y) coordinates → Identity) and mapping the two consensus-type
//     metadata shapes back and forth is not worth the review burden. If a
//     config update flips ConsensusType away from "BDLS" we return nil and
//     let the generic channel-config machinery handle it — Fabric will
//     refuse the transition at a higher layer.
// ---------------------------------------------------------------------------

// ValidateConsensusMetadata is invoked by the Registrar when a config
// transaction touches the orderer group on this channel. A nil return means
// "this metadata update is acceptable"; any non-nil error is surfaced to
// the submitter and the transaction is rejected.
//
// The signature matches consensus.MetadataValidator so that
// multichannel/chainsupport.go picks us up via its
//
//	cs.MetadataValidator, ok = cs.Chain.(consensus.MetadataValidator)
//
// type assertion without any extra wiring.
func (c *Chain) ValidateConsensusMetadata(oldOrdererConfig, newOrdererConfig channelconfig.Orderer, newChannel bool) error {
	if newOrdererConfig == nil {
		return errors.New("bdls metadata validator: nil new channel config")
	}

	// Metadata was not touched by this config update — nothing for us to
	// verify. We return nil rather than panic (etcdraft panics on this
	// path) because a metadata-less update is entirely legitimate when an
	// operator changes, say, BatchTimeout without touching consenters.
	if newOrdererConfig.ConsensusMetadata() == nil {
		return nil
	}

	// If the consensus type is changing away from BDLS we don't validate
	// the new metadata — it's not in our proto shape any more. The upper
	// channel-config layer decides whether the transition itself is
	// legal.
	if newOrdererConfig.ConsensusType() != "BDLS" {
		return nil
	}

	newMD, err := parseConfigMetadata(newOrdererConfig.ConsensusMetadata())
	if err != nil {
		return errors.Wrap(err, "bdls metadata validator: parsing new ConfigMetadata")
	}

	// buildBDLSConfig does the heavy lifting — it walks Consenters,
	// extracts public keys, maps them to bdlslib.Identity via
	// DefaultPubKeyToIdentity, and sanity-checks the participant count
	// against bdlslib.ConfigMinimumParticipants. We call it with a
	// currentHeight of 0 because we're validating the *shape* of the new
	// metadata, not simulating a run at a specific height.
	if _, err := buildBDLSConfig(newMD, 0); err != nil {
		return errors.Wrap(err, "bdls metadata validator: new ConfigMetadata is not a valid BDLS config")
	}

	// Enforce the 3f+1 floor explicitly even though buildBDLSConfig
	// already does it. Having the check in two places is cheap and means
	// a future refactor that weakens buildBDLSConfig cannot silently
	// drop the quorum guarantee.
	if len(newMD.Consenters) < bdlslib.ConfigMinimumParticipants {
		return errors.Errorf("bdls metadata validator: %d consenters requested, BDLS requires at least %d",
			len(newMD.Consenters), bdlslib.ConfigMinimumParticipants)
	}

	if newChannel {
		// On a fresh channel we have nothing to diff against. The shape
		// check above is sufficient.
		return nil
	}

	// Reject anything that would leave BDLS unable to make progress from
	// its five Δ knobs. Negative durations already round-trip to zero
	// via msToDuration, but an operator who sets Delta0 to 0 explicitly
	// (meaning "library default") after a channel has been running with
	// a tuned value is still allowed — that's a deliberate reset.
	if opt := newMD.Options; opt != nil {
		for name, v := range map[string]int64{
			"delta0_ms":       opt.Delta0Ms,
			"delta1_ms":       opt.Delta1Ms,
			"delta_prime1_ms": opt.DeltaPrime1Ms,
			"delta2_ms":       opt.Delta2Ms,
			"delta3_ms":       opt.Delta3Ms,
			"latency_ms":      opt.LatencyMs,
		} {
			if v < 0 {
				return errors.Errorf("bdls metadata validator: %s is negative (%d); use 0 to inherit the library default", name, v)
			}
		}
	}

	// Old config is only useful for diffing — skip if it's absent. The
	// Registrar always passes both on a real update; nil old-config
	// happens in tests.
	if oldOrdererConfig == nil || oldOrdererConfig.ConsensusMetadata() == nil {
		return nil
	}
	if _, err := parseConfigMetadata(oldOrdererConfig.ConsensusMetadata()); err != nil {
		// An unparseable *old* metadata is a "how did this ever commit?"
		// state we cannot fix from here. Log-worthy for operators but
		// not a reason to block a *new* update that is itself valid —
		// that would just trap the channel forever.
		if c != nil && c.logger != nil {
			c.logger.Warnf("bdls metadata validator: could not parse previously committed ConfigMetadata: %v", err)
		}
	}

	return nil
}
