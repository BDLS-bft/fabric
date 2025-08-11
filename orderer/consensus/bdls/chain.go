package bdls

import (
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
)

// Chain implements consensus.Chain interface for BDLS.
type Chain struct{}

// NewChain constructs a chain object.
func NewChain() *Chain {
	return &Chain{}
}

func (c *Chain) Start() {}

func (c *Chain) Order(env *common.Envelope, configSeq uint64) error {
	return nil
}

func (c *Chain) Configure(env *common.Envelope, configSeq uint64) error {
	return nil
}

func (c *Chain) WaitReady() error {
	return nil
}

func (c *Chain) Errored() <-chan struct{} {
	return make(chan struct{})
}

func (c *Chain) Halt() {}
