package bdls

import (
	"crypto/ecdsa"
	"time"
)

// Logger defines a generic logging interface for the consensus library.
type Logger interface {
	Debugf(template string, args ...interface{})
	Infof(template string, args ...interface{})
	Warnf(template string, args ...interface{})
	Errorf(template string, args ...interface{})
}

// Ticker abstracts a time source that produces periodic events.
type Ticker interface {
	Chan() <-chan time.Time
	Stop()
}

// TickerFactory constructs a Ticker for the provided interval.
type TickerFactory func(time.Duration) Ticker

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
	// Signer
	Signer Signer
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

	// Comm is the communication interface for sending messages.
	Comm Transmitter

	// Deliver is called when a block is committed.
	Deliver func(State) error

	// Logger is the logging interface.
	Logger Logger

	// TickInterval configures how frequently Consensus should advance its internal
	// timers. If zero, a default interval is used.
	TickInterval time.Duration

	// NewTicker, if supplied, is used to construct the internal ticker that drives
	// Consensus timeouts. If nil, time.NewTicker is used.
	NewTicker TickerFactory
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

	if c.Signer == nil {
		return ErrConfigSigner
	}

	if c.Comm == nil {
		return ErrConfigComm
	}

	if c.Deliver == nil {
		return ErrConfigDeliver
	}

	if len(c.Participants) < ConfigMinimumParticipants {
		return ErrConfigParticipants
	}

	return nil
}
