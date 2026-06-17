/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/protoutil"
	"google.golang.org/protobuf/proto"
)

// blockCreator assembles the next Fabric block from a batch of envelopes and
// threads the previous-block hash forward so the ledger chain stays
// consistent across restarts.
//
// It is intentionally a straight port of orderer/consensus/etcdraft's
// blockCreator — the two consenters need the same block-assembly semantics,
// and diverging would invite subtle header drift that no test catches until
// cross-consenter migration time. The only difference is that we return
// errors instead of panicking, matching the "BDLS should not crash the
// orderer" rule we enforced in PR-A2 on the library side.
type blockCreator struct {
	hash   []byte
	number uint64

	logger *flogging.FabricLogger
}

// createNextBlock builds a new block whose header hashes back to the
// previously created block. The caller (chain.go) is responsible for
// eventually calling support.WriteBlock / WriteBlockSync so the ledger and
// the blockCreator's internal counters move forward together.
//
// Unlike the etcdraft version this returns an error instead of panicking on
// a marshal failure. chain.go surfaces the error on the Errored channel so
// the whole orderer process stays up.
func (bc *blockCreator) createNextBlock(envs []*cb.Envelope) (*cb.Block, error) {
	data := &cb.BlockData{
		Data: make([][]byte, len(envs)),
	}
	for i, env := range envs {
		raw, err := proto.Marshal(env)
		if err != nil {
			if bc.logger != nil {
				bc.logger.Errorf("bdls: failed to marshal envelope %d of %d: %v", i, len(envs), err)
			}
			return nil, err
		}
		data.Data[i] = raw
	}

	bc.number++
	block := protoutil.NewBlock(bc.number, bc.hash)
	block.Header.DataHash = protoutil.ComputeBlockDataHash(data)
	block.Data = data

	bc.hash = protoutil.BlockHeaderHash(block.Header)
	return block, nil
}

// advance is used after a block has been committed to the ledger by some
// path other than createNextBlock — for example, a catch-up via BlockPuller.
// It keeps the creator's counters in sync with the ledger head so the next
// locally cut block hashes back to the right place.
func (bc *blockCreator) advance(block *cb.Block) {
	if block == nil || block.Header == nil {
		return
	}
	bc.number = block.Header.Number
	bc.hash = protoutil.BlockHeaderHash(block.Header)
}
