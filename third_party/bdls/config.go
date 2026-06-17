package bdls

import (
	"crypto/ecdsa"
	"time"
)

const (
	// ConfigMinimumParticipants is the minimum number of participant allow in consensus protocol
	ConfigMinimumParticipants = 4
)

// Config is to config the parameters of BDLS consensus protocol
type Config struct {
	// the starting time point for consensus
	Epoch time.Time
	// CurrentHeight
	CurrentHeight uint64
    // PrivateKey
    PrivateKey *ecdsa.PrivateKey

    // SignDigest is an alternative signing mechanism when PrivateKey is not available.
    // It receives the BDLS-produced digest and returns the raw ECDSA (r, s) byte slices.
    // When SignDigest is provided, PublicKey must also be set.
    SignDigest func(digest []byte) (rBytes, sBytes []byte, err error)

    // PublicKey is required when using SignDigest. It is used to populate X/Y and derive identity.
    PublicKey *ecdsa.PublicKey
	// Consensus Group
	Participants []Identity
	// EnableCommitUnicast sets to true to enable <commit> message to be delivered via unicast
	// if not(by default), <commit> message will be broadcasted
	EnableCommitUnicast bool

	// StateCompare is a function from user to compare states,
	// The result will be 0 if a==b, -1 if a < b, and +1 if a > b.
	// Usually this will lead to block header comparsion in blockchain, or replication log in database,
	// users should check fields in block header to make comparison.
	StateCompare func(a State, b State) int

	// StateValidate is a function from user to validate the integrity of
	// state data.
	StateValidate func(State) bool

	// MessageValidator is an external validator to be called when a message inputs into ReceiveMessage
	MessageValidator func(c *Consensus, m *Message, signed *SignedProto) bool

	// MessageOutCallback will be called if not nil before a message send out
	MessageOutCallback func(m *Message, signed *SignedProto)

	// Identity derviation from ecdsa.PublicKey
	// (optional). Default to DefaultPubKeyToIdentity
	PubKeyToIdentity func(pubkey *ecdsa.PublicKey) (ret Identity)

	// ReliableDecide turns on strongly-reliable <decide> propagation
	// (Section 9 / Remark 2 of eprint 2019/1460). When enabled, a node
	// will only advance to the next height after it has observed a
	// <decide> for the current height re-broadcast by at least 2t+1
	// distinct participants (tracked by the outer signer). This closes
	// the Scenario-I/II/III forking window that the single-flood
	// propagate() path leaves open. Defaults to false so existing
	// embedders keep their current behaviour; new embedders (including
	// Fabric) should set this to true.
	ReliableDecide bool

	// Delta0..Delta3 + DeltaPrime1 are independent tuning knobs for the
	// five timeouts the paper defines in Section 8.2. A zero value means
	// "derive from Latency as before", so this field is purely additive
	// and unset configs behave identically to pre-PR-A3 code.
	//
	//   Delta0       — round-change stage timeout
	//   Delta1       — lock stage timeout for non-leader
	//   DeltaPrime1  — lock stage timeout for leader (Δ'_1)
	//   Delta2       — commit stage timeout
	//   Delta3       — lock-release stage timeout
	Delta0      time.Duration
	Delta1      time.Duration
	DeltaPrime1 time.Duration
	Delta2      time.Duration
	Delta3      time.Duration
}

// VerifyConfig verifies the integrity of this config when creating new consensus object
func VerifyConfig(c *Config) error {
	if c.Epoch.IsZero() {
		return ErrConfigEpoch
	}

	if c.StateCompare == nil {
		return ErrConfigStateCompare
	}

	if c.StateValidate == nil {
		return ErrConfigStateValidate
	}

    // Either a private key must be provided, or a signer callback with public key.
    if c.PrivateKey == nil {
        if c.SignDigest == nil || c.PublicKey == nil {
            return ErrConfigPrivateKey
        }
    }

	if len(c.Participants) < ConfigMinimumParticipants {
		return ErrConfigParticipants
	}

	return nil
}
