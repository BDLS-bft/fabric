/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"testing"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/stretchr/testify/require"
)

func TestBlockCreator_CreateNextBlock(t *testing.T) {
	genesis := protoutil.NewBlock(0, nil)
	bc := &blockCreator{
		hash:   protoutil.BlockHeaderHash(genesis.Header),
		number: 0,
	}

	envs := []*cb.Envelope{
		{Payload: []byte("tx1")},
		{Payload: []byte("tx2")},
	}
	blk, err := bc.createNextBlock(envs)
	require.NoError(t, err)
	require.EqualValues(t, 1, blk.Header.Number)
	require.NotEmpty(t, blk.Header.PreviousHash)
	require.Len(t, blk.Data.Data, 2)

	// PreviousHash must be the genesis block's header hash.
	require.Equal(t, protoutil.BlockHeaderHash(genesis.Header), blk.Header.PreviousHash)
}

func TestBlockCreator_HashChaining(t *testing.T) {
	bc := &blockCreator{
		hash:   []byte("seed"),
		number: 0,
	}

	blk1, err := bc.createNextBlock([]*cb.Envelope{{Payload: []byte("a")}})
	require.NoError(t, err)
	blk2, err := bc.createNextBlock([]*cb.Envelope{{Payload: []byte("b")}})
	require.NoError(t, err)

	require.EqualValues(t, 1, blk1.Header.Number)
	require.EqualValues(t, 2, blk2.Header.Number)
	// Block 2's PreviousHash must be block 1's header hash.
	require.Equal(t, protoutil.BlockHeaderHash(blk1.Header), blk2.Header.PreviousHash)
}

func TestBlockCreator_Advance(t *testing.T) {
	bc := &blockCreator{number: 0, hash: []byte("old")}

	// Simulate a catch-up block.
	catchUp := protoutil.NewBlock(10, []byte("whatever"))
	bc.advance(catchUp)

	require.EqualValues(t, 10, bc.number)
	require.Equal(t, protoutil.BlockHeaderHash(catchUp.Header), bc.hash)

	// Next locally-cut block should be 11.
	blk, err := bc.createNextBlock([]*cb.Envelope{{Payload: []byte("c")}})
	require.NoError(t, err)
	require.EqualValues(t, 11, blk.Header.Number)
}

func TestBlockCreator_Advance_NilBlock(t *testing.T) {
	bc := &blockCreator{number: 5, hash: []byte("keep")}
	bc.advance(nil)
	require.EqualValues(t, 5, bc.number, "nil block should be a no-op")
}
