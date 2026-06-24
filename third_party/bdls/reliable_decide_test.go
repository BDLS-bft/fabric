package bdls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"testing"
	"time"

	proto "github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
)

// buildReliableDecideFixture builds a 4-participant consensus group and
// returns (a) the private keys, (b) the participant identities, (c) a
// receiver consensus instance owned by participant[1], and (d) a function
// that marshals a <decide> message signed by the provided signer. The
// decide wraps 2t+1 = 3 valid <commit> proofs over the same state, which
// is what verifyDecideMessage requires for finality.
func buildReliableDecideFixture(t *testing.T, reliable bool) (keys []*ecdsa.PrivateKey, ids []Identity, c *Consensus, mkDecide func(signer *ecdsa.PrivateKey) []byte) {
	t.Helper()

	keys = make([]*ecdsa.PrivateKey, 4)
	ids = make([]Identity, 4)
	for i := range keys {
		k, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
		assert.Nil(t, err)
		keys[i] = k
		ids[i] = DefaultPubKeyToIdentity(&k.PublicKey)
	}

	// Receiver is participant[1]. participants[0] is the round-0 leader, so
	// the receiver is a non-leader — exactly the "I got the decide from the
	// leader, now I need to forward it reliably" situation.
	cfg := &Config{
		Epoch:          time.Now(),
		CurrentHeight:  0,
		PrivateKey:     keys[1],
		Participants:   ids,
		StateCompare:   func(a, b State) int { return bytes.Compare(a, b) },
		StateValidate:  func(State) bool { return true },
		ReliableDecide: reliable,
	}
	var err error
	c, err = NewConsensus(cfg)
	assert.Nil(t, err)

	state := []byte("reliable-decide-target-state")

	// Build 2t+1 valid commits, each signed by a distinct participant.
	buildCommit := func(signer *ecdsa.PrivateKey) *SignedProto {
		m := &Message{Type: MessageType_Commit, Height: 1, Round: 0, State: state}
		sp := new(SignedProto)
		sp.Version = ProtocolVersion
		sp.Sign(m, signer)
		return sp
	}
	commits := []*SignedProto{
		buildCommit(keys[0]),
		buildCommit(keys[1]),
		buildCommit(keys[2]),
	}

	mkDecide = func(signer *ecdsa.PrivateKey) []byte {
		decide := &Message{
			Type:   MessageType_Decide,
			Height: 1,
			Round:  0,
			State:  state,
			Proof:  commits,
		}
		sp := new(SignedProto)
		sp.Version = ProtocolVersion
		sp.Sign(decide, signer)
		out, err := proto.Marshal(sp)
		assert.Nil(t, err)
		return out
	}
	return keys, ids, c, mkDecide
}

// TestReliableDecide_GatesAdvancementOnDistinctSigners exercises B2 from the
// integration plan: with Config.ReliableDecide enabled, a single leader
// flood MUST NOT be enough to advance the height. Only after at least 2t+1
// distinct outer signers have re-broadcast the same decide does the
// receiver advance. This is the mechanism that closes the Section-9 fork
// window for Scenarios I/II/III.
func TestReliableDecide_GatesAdvancementOnDistinctSigners(t *testing.T) {
	keys, _, c, mkDecide := buildReliableDecideFixture(t, true)

	// Initial height is 0, nothing decided yet.
	h0, _, _ := c.CurrentState()
	assert.Equal(t, uint64(0), h0)

	// Feed the leader-signed decide. After this call the receiver has:
	//   - signers: {leader=keys[0]}
	//   - its own re-broadcast has been drained via loopback, adding {self=keys[1]}
	// i.e. 2 distinct signers, which is below 2t+1 = 3. No advance yet.
	err := c.ReceiveMessage(mkDecide(keys[0]), time.Now())
	assert.Nil(t, err)
	h1, _, _ := c.CurrentState()
	assert.Equal(t, uint64(0), h1, "first-leader flood must not advance height under reliable mode")

	// Feed a re-signed copy from a third participant. Now signers =
	// {keys[0], keys[1], keys[2]}, hitting the 2t+1 threshold exactly.
	err = c.ReceiveMessage(mkDecide(keys[2]), time.Now())
	assert.Nil(t, err)
	h2, _, s := c.CurrentState()
	assert.Equal(t, uint64(1), h2, "height must advance once 2t+1 distinct signers seen")
	assert.True(t, bytes.Equal(s, []byte("reliable-decide-target-state")))

	// The in-flight tracking map should be garbage-collected on advance.
	assert.Empty(t, c.decideAcks)
	assert.Empty(t, c.decideRebroadcast)
}

// TestReliableDecide_Disabled_AdvancesOnSingleFlood is the contrast case:
// when ReliableDecide is off (pre-A3 behaviour) a single leader flood
// immediately advances the height. This documents what PR-A3 is fixing.
func TestReliableDecide_Disabled_AdvancesOnSingleFlood(t *testing.T) {
	keys, _, c, mkDecide := buildReliableDecideFixture(t, false)

	err := c.ReceiveMessage(mkDecide(keys[0]), time.Now())
	assert.Nil(t, err)
	h, _, _ := c.CurrentState()
	assert.Equal(t, uint64(1), h, "legacy path advances on the very first decide — Section 9 fork risk")
}

// TestReliableDecide_IgnoresUnknownOuterSigner ensures the lax verifier
// still rejects an outer signature from a key that is not part of the
// participant set. Lax doesn't mean lawless.
func TestReliableDecide_IgnoresUnknownOuterSigner(t *testing.T) {
	_, _, c, mkDecide := buildReliableDecideFixture(t, true)

	stranger, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
	assert.Nil(t, err)

	err = c.ReceiveMessage(mkDecide(stranger), time.Now())
	// receiveMessage calls verifyMessage first, which rejects unknown
	// participants before the decide-specific check even runs. Either
	// error is acceptable so long as the message is refused.
	assert.ErrorIs(t, err, ErrMessageUnknownParticipant)
	h, _, _ := c.CurrentState()
	assert.Equal(t, uint64(0), h, "outer signer outside participant set must not advance height")
}

// TestUpdateParticipants_BetweenHeights covers the happy path for the new
// reconfiguration API (B3): a fresh consensus with no in-flight state can
// swap to a larger participant set and numIdentities tracks accordingly.
func TestUpdateParticipants_BetweenHeights(t *testing.T) {
	// Initial 4-node group.
	keys := make([]*ecdsa.PrivateKey, 4)
	ids := make([]Identity, 4)
	for i := range keys {
		k, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
		assert.Nil(t, err)
		keys[i] = k
		ids[i] = DefaultPubKeyToIdentity(&k.PublicKey)
	}

	cfg := &Config{
		Epoch:         time.Now(),
		CurrentHeight: 0,
		PrivateKey:    keys[0],
		Participants:  ids,
		StateCompare:  func(a, b State) int { return bytes.Compare(a, b) },
		StateValidate: func(State) bool { return true },
	}
	c, err := NewConsensus(cfg)
	assert.Nil(t, err)
	assert.Equal(t, 4, c.numIdentities)

	// Add a fifth consenter (Fabric channel-config add path).
	extra, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
	assert.Nil(t, err)
	next := append(append([]Identity{}, ids...), DefaultPubKeyToIdentity(&extra.PublicKey))

	err = c.UpdateParticipants(next)
	assert.Nil(t, err)
	assert.Equal(t, 5, c.numIdentities)
	assert.Len(t, c.participants, 5)
	// t() = (5-1)/3 = 1 → quorum 2t+1 = 3 still sane for the new group.
	assert.Equal(t, 1, c.t())
}

// TestUpdateParticipants_HeightInFlight covers the refusal path: once any
// state for the current height has been recorded — a round-change vote, a
// lock, or a non-zero round number — UpdateParticipants must refuse so
// ongoing consensus cannot be disrupted mid-stream.
func TestUpdateParticipants_HeightInFlight(t *testing.T) {
	keys := make([]*ecdsa.PrivateKey, 4)
	ids := make([]Identity, 4)
	for i := range keys {
		k, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
		assert.Nil(t, err)
		keys[i] = k
		ids[i] = DefaultPubKeyToIdentity(&k.PublicKey)
	}

	cfg := &Config{
		Epoch:         time.Now(),
		CurrentHeight: 0,
		PrivateKey:    keys[0],
		Participants:  ids,
		StateCompare:  func(a, b State) int { return bytes.Compare(a, b) },
		StateValidate: func(State) bool { return true },
	}
	c, err := NewConsensus(cfg)
	assert.Nil(t, err)

	// Force some in-flight state by bumping the round number.
	c.switchRound(1)
	// The round number jump alone is enough to be considered "in flight".
	err = c.UpdateParticipants(ids)
	assert.ErrorIs(t, err, ErrUpdateParticipantsHeightInFlight)

	// Refuse also when the minimum-participants invariant would break.
	err = c.UpdateParticipants(ids[:2])
	assert.ErrorIs(t, err, ErrConfigParticipants)
}

// TestDeltaKnobs_OverrideLatencyDefaults verifies G3: when Config.Delta*
// values are set, they replace the latency-derived defaults, and when
// they are zero the pre-A3 behaviour is preserved.
func TestDeltaKnobs_OverrideLatencyDefaults(t *testing.T) {
	// Baseline — no Δ overrides. Must match pre-A3 durations.
	keys := make([]*ecdsa.PrivateKey, 4)
	ids := make([]Identity, 4)
	for i := range keys {
		k, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
		assert.Nil(t, err)
		keys[i] = k
		ids[i] = DefaultPubKeyToIdentity(&k.PublicKey)
	}
	makeCfg := func(apply func(*Config)) *Config {
		cfg := &Config{
			Epoch:         time.Now(),
			CurrentHeight: 0,
			PrivateKey:    keys[0],
			Participants:  ids,
			StateCompare:  func(a, b State) int { return bytes.Compare(a, b) },
			StateValidate: func(State) bool { return true },
		}
		if apply != nil {
			apply(cfg)
		}
		return cfg
	}

	// Baseline: Δ0 default = 2*latency, Δ1 default = 4*latency, Δ2 = 2*latency, Δ3 = 2*latency
	cBase, err := NewConsensus(makeCfg(nil))
	assert.Nil(t, err)
	cBase.SetLatency(100 * time.Millisecond)
	assert.Equal(t, 200*time.Millisecond, cBase.roundchangeDuration(0))
	assert.Equal(t, 400*time.Millisecond, cBase.lockDuration(0))
	assert.Equal(t, 200*time.Millisecond, cBase.commitDuration(0))
	assert.Equal(t, 200*time.Millisecond, cBase.lockReleaseDuration(0))

	// Overridden knobs.
	cOverride, err := NewConsensus(makeCfg(func(c *Config) {
		c.Delta0 = 50 * time.Millisecond
		c.Delta1 = 150 * time.Millisecond
		c.Delta2 = 75 * time.Millisecond
		c.Delta3 = 25 * time.Millisecond
	}))
	assert.Nil(t, err)
	cOverride.SetLatency(100 * time.Millisecond)
	assert.Equal(t, 50*time.Millisecond, cOverride.roundchangeDuration(0))
	assert.Equal(t, 150*time.Millisecond, cOverride.lockDuration(0))
	assert.Equal(t, 75*time.Millisecond, cOverride.commitDuration(0))
	assert.Equal(t, 25*time.Millisecond, cOverride.lockReleaseDuration(0))

	// Exponential round backoff must still apply: round 2 → base << 2.
	assert.Equal(t, 200*time.Millisecond, cOverride.roundchangeDuration(2)) // 50 * 4
}
