package bdls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLazyStartSuppressesIdleRoundChangeUntilPropose(t *testing.T) {
	keys := make([]*ecdsa.PrivateKey, 4)
	participants := make([]Identity, 4)
	for i := range keys {
		key, err := ecdsa.GenerateKey(S256Curve, rand.Reader)
		assert.Nil(t, err)
		keys[i] = key
		participants[i] = DefaultPubKeyToIdentity(&key.PublicKey)
	}

	var outbound []*Message
	consensus, err := NewConsensus(&Config{
		Epoch:        time.Now(),
		PrivateKey:   keys[0],
		Participants: participants,
		StateCompare: func(a, b State) int { return bytes.Compare(a, b) },
		StateValidate: func(State) bool {
			return true
		},
		LazyStart: true,
		MessageOutCallback: func(m *Message, _ *SignedProto) {
			outbound = append(outbound, &Message{
				Type:   m.Type,
				Height: m.Height,
				Round:  m.Round,
				State:  append(State(nil), m.State...),
			})
		},
	})
	assert.Nil(t, err)
	assert.Empty(t, outbound)

	assert.Nil(t, consensus.Update(time.Now().Add(time.Hour)))
	assert.Empty(t, outbound)

	state := State("candidate-block")
	consensus.Propose(state)
	if assert.Len(t, outbound, 1) {
		assert.Equal(t, MessageType_RoundChange, outbound[0].Type)
		assert.Equal(t, uint64(1), outbound[0].Height)
		assert.Equal(t, uint64(0), outbound[0].Round)
		assert.Equal(t, []byte(state), []byte(outbound[0].State))
	}
}
