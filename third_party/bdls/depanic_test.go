package bdls

import (
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestUpdate_TimeoutNotSet_ReturnsErrorInsteadOfPanic exercises the four
// former panic sites in (*Consensus).Update when a stage is entered without
// its corresponding timeout initialised. Each case must now surface a typed
// error rather than crashing the embedder (see PR-A2).
func TestUpdate_TimeoutNotSet_ReturnsErrorInsteadOfPanic(t *testing.T) {
	// Build a quorum of ConfigMinimumParticipants keys so VerifyConfig passes.
	quorum := make([]*ecdsa.PublicKey, 0, ConfigMinimumParticipants-1)
	for i := 0; i < ConfigMinimumParticipants-1; i++ {
		pk, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
		assert.Nil(t, err)
		quorum = append(quorum, &pk.PublicKey)
	}

	cases := []struct {
		name    string
		stage   consensusStage
		wantErr error
		zeroOut func(c *Consensus)
	}{
		{
			name:    "roundchanging without rcTimeout",
			stage:   stageRoundChanging,
			wantErr: ErrRoundChangeTimeoutNotSet,
			zeroOut: func(c *Consensus) { c.rcTimeout = time.Time{} },
		},
		{
			name:    "lock without lockTimeout",
			stage:   stageLock,
			wantErr: ErrLockTimeoutNotSet,
			zeroOut: func(c *Consensus) { c.lockTimeout = time.Time{} },
		},
		{
			name:    "commit without commitTimeout",
			stage:   stageCommit,
			wantErr: ErrCommitTimeoutNotSet,
			zeroOut: func(c *Consensus) { c.commitTimeout = time.Time{} },
		},
		{
			name:    "lockRelease without lockReleaseTimeout",
			stage:   stageLockRelease,
			wantErr: ErrLockReleaseTimeoutNotSet,
			zeroOut: func(c *Consensus) { c.lockReleaseTimeout = time.Time{} },
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := createConsensus(t, 1, 0, quorum)
			// Force the stage and clear its timeout field to reproduce the
			// pre-condition that formerly caused a panic.
			c.currentRound.Stage = tc.stage
			tc.zeroOut(c)

			// The call must return the expected sentinel error and NOT panic.
			var err error
			assert.NotPanics(t, func() {
				err = c.Update(time.Now())
			})
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// TestReceiveMessage_MalformedBytes_ReturnsErrorNotPanic feeds garbage bytes
// into ReceiveMessage. The proto-unmarshal path already returned an error,
// but the B1 regression asks that no downstream message path can panic, so
// assert the public surface behaves accordingly.
func TestReceiveMessage_MalformedBytes_ReturnsErrorNotPanic(t *testing.T) {
	quorum := make([]*ecdsa.PublicKey, 0, ConfigMinimumParticipants-1)
	for i := 0; i < ConfigMinimumParticipants-1; i++ {
		pk, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
		assert.Nil(t, err)
		quorum = append(quorum, &pk.PublicKey)
	}
	c := createConsensus(t, 1, 0, quorum)

	garbage := []byte{0xff, 0x00, 0xde, 0xad, 0xbe, 0xef}
	var err error
	assert.NotPanics(t, func() {
		err = c.ReceiveMessage(garbage, time.Now())
	})
	assert.NotNil(t, err, "malformed bytes must surface an error")
}

// TestPendingError_SurfacedFromPublicEntrypoints verifies the pendingError
// plumbing: a synthetic internal failure recorded via setError is drained on
// the next public call so the embedder never silently loses a formerly-
// panicking condition.
func TestPendingError_SurfacedFromPublicEntrypoints(t *testing.T) {
	quorum := make([]*ecdsa.PublicKey, 0, ConfigMinimumParticipants-1)
	for i := 0; i < ConfigMinimumParticipants-1; i++ {
		pk, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
		assert.Nil(t, err)
		quorum = append(quorum, &pk.PublicKey)
	}
	c := createConsensus(t, 1, 0, quorum)

	sentinel := errors.New("synthetic hot-path failure")
	c.setError(sentinel)
	// A second setError call must be dropped: the first cause wins.
	c.setError(errors.New("second error should be ignored"))

	// Update with a sane rcTimeout so the stage switch itself does not error.
	c.rcTimeout = time.Now().Add(time.Hour)
	err := c.Update(time.Now())
	assert.ErrorIs(t, err, sentinel)

	// After draining, the slot is clear.
	assert.Nil(t, c.pendingError)
	assert.Nil(t, c.consumePendingError())
}
