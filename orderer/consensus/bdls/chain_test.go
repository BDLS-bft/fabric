/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	bdlslib "github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric/common/util"
	"github.com/hyperledger/fabric/orderer/consensus/mocks"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSignBlockBFTUsesIdentifierHeader(t *testing.T) {
	block := protoutil.NewBlock(7, []byte("previous"))
	consenterMetadata := []byte("bdls metadata")
	signature := []byte("signature")

	var signedPayload []byte
	support := &mocks.FakeConsenterSupport{}
	support.SignCalls(func(message []byte) ([]byte, error) {
		signedPayload = append([]byte(nil), message...)
		return signature, nil
	})

	chain := &Chain{
		support:            support,
		selfConsenterID:    42,
		lastConfigBlockNum: 3,
	}

	signatureMetadata, err := chain.createBlockSignatureBFT(block, consenterMetadata)
	require.NoError(t, err)
	chain.setBlockSignatureMetadata(block, signatureMetadata)
	require.Equal(t, 1, support.SignCallCount())

	writtenSignatureMetadata := &common.Metadata{}
	require.NoError(t, proto.Unmarshal(block.Metadata.Metadata[common.BlockMetadataIndex_SIGNATURES], writtenSignatureMetadata))
	require.Len(t, writtenSignatureMetadata.Signatures, 1)

	metadataSignature := writtenSignatureMetadata.Signatures[0]
	require.Empty(t, metadataSignature.SignatureHeader)
	require.Equal(t, signature, metadataSignature.Signature)

	identifierHeader := &common.IdentifierHeader{}
	require.NoError(t, proto.Unmarshal(metadataSignature.IdentifierHeader, identifierHeader))
	require.EqualValues(t, 42, identifierHeader.Identifier)

	ordererBlockMetadata := &common.OrdererBlockMetadata{}
	require.NoError(t, proto.Unmarshal(writtenSignatureMetadata.Value, ordererBlockMetadata))
	require.EqualValues(t, 3, ordererBlockMetadata.LastConfig.Index)

	wrappedMetadata := &common.Metadata{}
	require.NoError(t, proto.Unmarshal(ordererBlockMetadata.ConsenterMetadata, wrappedMetadata))
	require.Equal(t, consenterMetadata, wrappedMetadata.Value)

	require.Equal(t, util.ConcatenateBytes(
		writtenSignatureMetadata.Value,
		metadataSignature.IdentifierHeader,
		protoutil.BlockHeaderBytes(block.Header),
	), signedPayload)
}

func TestCollectBlockSignaturesRequiresQuorum(t *testing.T) {
	block := protoutil.NewBlock(9, []byte("previous"))
	ordererMetadataBytes := []byte("orderer metadata")
	chain := &Chain{
		tickInterval:          time.Millisecond,
		blockSignatureTimeout: 25 * time.Millisecond,
		pendingBlockSignature: make(map[string]map[uint32]*common.MetadataSignature),
		signatureQuorum:       3,
		haltC:                 make(chan struct{}),
	}

	for _, id := range []uint32{3, 1} {
		chain.recordBlockSignature(block.Header, ordererMetadataBytes, metadataSignatureForID(t, id))
	}
	_, err := chain.collectBlockSignatures(block, ordererMetadataBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "waiting for 3 signatures")

	chain.recordBlockSignature(block.Header, ordererMetadataBytes, metadataSignatureForID(t, 2))
	metadata, err := chain.collectBlockSignatures(block, ordererMetadataBytes)
	require.NoError(t, err)
	require.Equal(t, ordererMetadataBytes, metadata.Value)
	require.Len(t, metadata.Signatures, 3)

	var ids []uint32
	for _, sig := range metadata.Signatures {
		id, err := signatureConsenterID(sig)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.Equal(t, []uint32{1, 2, 3}, ids)
}

func TestCollectBlockSignaturesWakesOnSignature(t *testing.T) {
	block := protoutil.NewBlock(10, []byte("previous"))
	ordererMetadataBytes := []byte("orderer metadata")
	chain := &Chain{
		blockSignatureTimeout: time.Second,
		pendingBlockSignature: make(map[string]map[uint32]*common.MetadataSignature),
		blockSignatureC:       make(chan struct{}, 1),
		signatureQuorum:       2,
		haltC:                 make(chan struct{}),
	}

	chain.recordBlockSignature(block.Header, ordererMetadataBytes, metadataSignatureForID(t, 1))
	errC := make(chan error, 1)
	go func() {
		metadata, err := chain.collectBlockSignatures(block, ordererMetadataBytes)
		if err != nil {
			errC <- err
			return
		}
		if len(metadata.Signatures) != 2 {
			errC <- fmt.Errorf("expected 2 signatures, got %d", len(metadata.Signatures))
			return
		}
		errC <- nil
	}()

	time.Sleep(10 * time.Millisecond)
	chain.recordBlockSignature(block.Header, ordererMetadataBytes, metadataSignatureForID(t, 2))

	select {
	case err := <-errC:
		require.NoError(t, err)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("collectBlockSignatures did not wake after signature notification")
	}
}

func TestSignalDecideCheckCoalesces(t *testing.T) {
	chain := &Chain{decideC: make(chan struct{}, 1)}

	chain.signalDecideCheck()
	chain.signalDecideCheck()

	require.Len(t, chain.decideC, 1)
	select {
	case <-chain.decideC:
	default:
		t.Fatal("expected decide check signal")
	}
	select {
	case <-chain.decideC:
		t.Fatal("expected coalesced single decide check signal")
	default:
	}
}

func TestIsStaleConsensusMessage(t *testing.T) {
	require.True(t, isStaleConsensusMessage(bdlslib.ErrRoundChangeHeightMismatch))
	require.True(t, isStaleConsensusMessage(bdlslib.ErrRoundChangeRoundLower))
	require.True(t, isStaleConsensusMessage(bdlslib.ErrLockHeightMismatch))
	require.True(t, isStaleConsensusMessage(fmt.Errorf("wrapped: %w", bdlslib.ErrDecideHeightLower)))
	require.True(t, isStaleConsensusMessage(bdlslib.ErrSelectHeightMismatch))
	require.True(t, isStaleConsensusMessage(bdlslib.ErrCommitHeightMismatch))
	require.True(t, isStaleConsensusMessage(bdlslib.ErrCommitRoundMismatch))
	require.True(t, isStaleConsensusMessage(bdlslib.ErrLockReleaseStatus))
	require.True(t, isStaleConsensusMessage(bdlslib.ErrCommitStatus))
	require.False(t, isStaleConsensusMessage(errors.New("malformed message")))
}

func TestBlockSignatureMessageRoundTrip(t *testing.T) {
	block := protoutil.NewBlock(11, []byte("previous"))
	signatureMetadata := &common.Metadata{
		Value: []byte("orderer metadata"),
		Signatures: []*common.MetadataSignature{
			metadataSignatureForID(t, 7),
		},
	}

	payload, err := marshalBlockSignatureMessage(block.Header, signatureMetadata)
	require.NoError(t, err)
	require.Contains(t, string(payload), blockSignatureMessageMagic)

	header, ordererMetadataBytes, signature, err := unmarshalBlockSignatureMessage(payload)
	require.NoError(t, err)
	require.True(t, proto.Equal(block.Header, header))
	require.Equal(t, signatureMetadata.Value, ordererMetadataBytes)

	id, err := signatureConsenterID(signature)
	require.NoError(t, err)
	require.EqualValues(t, 7, id)
}

func TestCompactStateForBlockRoundTrip(t *testing.T) {
	blockBytes := []byte("block bytes")
	state, hash := compactStateForBlock(12, blockBytes)

	blockNumber, parsedHash, ok := parseCompactState(state)
	require.True(t, ok)
	require.EqualValues(t, 12, blockNumber)
	require.Equal(t, hash, parsedHash)
	require.Equal(t, sha256.Sum256(blockBytes), parsedHash)

	_, _, ok = parseCompactState([]byte("not compact"))
	require.False(t, ok)
}

func TestReceiveBlockProposalAndResolveDecidedState(t *testing.T) {
	blockBytes := []byte("marshalled block")
	state, hash := compactStateForBlock(4, blockBytes)
	payload := blockProposalPayload(4, hash, blockBytes)
	chain := &Chain{
		channelID:          "testchannel",
		pendingBlockByHash: make(map[[sha256.Size]byte]pendingBlockProposal),
	}

	_, compact, err := chain.resolveDecidedState(state)
	require.Error(t, err)
	require.True(t, compact)
	require.Contains(t, err.Error(), "not available yet")

	require.NoError(t, chain.receiveBlockProposal(payload, 2))
	resolved, compact, err := chain.resolveDecidedState(state)
	require.NoError(t, err)
	require.True(t, compact)
	require.Equal(t, blockBytes, resolved)

	resolved[0] = 'X'
	resolvedAgain, compact, err := chain.resolveDecidedState(state)
	require.NoError(t, err)
	require.True(t, compact)
	require.Equal(t, blockBytes, resolvedAgain, "resolver must return a defensive copy")
}

func TestReceiveBlockProposalProposesValidatedCompactState(t *testing.T) {
	genesis := protoutil.NewBlock(0, nil)
	block := testBlockWithPreviousHashAndEnvelopes(t, 1, protoutil.BlockHeaderHash(genesis.Header), &common.Envelope{
		Payload:   []byte("payload"),
		Signature: []byte("signature"),
	})
	blockBytes, err := proto.Marshal(block)
	require.NoError(t, err)
	state, hash := compactStateForBlock(1, blockBytes)

	var roundChangeStates [][]byte
	consensus := testLazyBDLSConsensus(t, func(m *bdlslib.Message, _ *bdlslib.SignedProto) {
		if m.Type == bdlslib.MessageType_RoundChange {
			roundChangeStates = append(roundChangeStates, append([]byte(nil), m.State...))
		}
	})
	require.Empty(t, roundChangeStates)

	support := &mocks.FakeConsenterSupport{}
	support.HeightReturns(1)
	support.BlockCalls(func(number uint64) *common.Block {
		require.EqualValues(t, 0, number)
		return genesis
	})
	chain := &Chain{
		channelID:          "testchannel",
		support:            support,
		bdls:               consensus,
		compactState:       true,
		pendingBlockByHash: make(map[[sha256.Size]byte]pendingBlockProposal),
		decideC:            make(chan struct{}, 1),
	}

	require.NoError(t, chain.receiveBlockProposal(blockProposalPayload(1, hash, blockBytes), 2))
	require.Len(t, roundChangeStates, 1)
	require.Equal(t, state, roundChangeStates[0])
}

func TestReceiveBlockProposalRejectsInvalidNextBlock(t *testing.T) {
	genesis := protoutil.NewBlock(0, nil)
	block := testBlockWithPreviousHashAndEnvelopes(t, 1, []byte("wrong-previous-hash"), &common.Envelope{
		Payload:   []byte("payload"),
		Signature: []byte("signature"),
	})
	blockBytes, err := proto.Marshal(block)
	require.NoError(t, err)
	_, hash := compactStateForBlock(1, blockBytes)

	var roundChangeStates [][]byte
	consensus := testLazyBDLSConsensus(t, func(m *bdlslib.Message, _ *bdlslib.SignedProto) {
		if m.Type == bdlslib.MessageType_RoundChange {
			roundChangeStates = append(roundChangeStates, append([]byte(nil), m.State...))
		}
	})

	support := &mocks.FakeConsenterSupport{}
	support.HeightReturns(1)
	support.BlockReturns(genesis)
	chain := &Chain{
		channelID:          "testchannel",
		support:            support,
		bdls:               consensus,
		compactState:       true,
		pendingBlockByHash: make(map[[sha256.Size]byte]pendingBlockProposal),
	}

	err = chain.receiveBlockProposal(blockProposalPayload(1, hash, blockBytes), 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatched previous hash")
	require.Empty(t, roundChangeStates)
}

func TestReceiveBlockProposalRejectsMalformedPayload(t *testing.T) {
	blockBytes := []byte("marshalled block")
	_, hash := compactStateForBlock(4, blockBytes)
	payload := blockProposalPayload(4, hash, blockBytes)
	chain := &Chain{
		channelID:          "testchannel",
		pendingBlockByHash: make(map[[sha256.Size]byte]pendingBlockProposal),
	}

	err := chain.receiveBlockProposal([]byte("wrong marker"), 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid block proposal marker")

	err = chain.receiveBlockProposal(payload[:len(blockProposalMessageMagic)+7], 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "short block proposal")

	payload[len(blockProposalMessageMagic)+8] ^= 0xff
	err = chain.receiveBlockProposal(payload, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatched hash")
}

func TestClearBlockProposalsThrough(t *testing.T) {
	chain := &Chain{pendingBlockByHash: make(map[[sha256.Size]byte]pendingBlockProposal)}
	for _, number := range []uint64{1, 2, 3} {
		_, hash := compactStateForBlock(number, []byte{byte(number)})
		chain.rememberBlockProposal(number, hash, []byte{byte(number)})
	}

	chain.clearBlockProposalsThrough(2)

	require.Len(t, chain.pendingBlockByHash, 1)
	for _, proposal := range chain.pendingBlockByHash {
		require.EqualValues(t, 3, proposal.number)
	}
}

func TestDeterministicSubmitterSelectsLowestConsenterID(t *testing.T) {
	peer7 := &peerAdapter{destination: 7}
	peer2 := &peerAdapter{destination: 2}

	id, peer := deterministicSubmitter(5, []*peerAdapter{peer7, peer2})

	require.EqualValues(t, 2, id)
	require.Same(t, peer2, peer)
}

func TestDeterministicSubmitterKeepsSelfWhenLowest(t *testing.T) {
	id, peer := deterministicSubmitter(1, []*peerAdapter{{destination: 2}, {destination: 3}})

	require.EqualValues(t, 1, id)
	require.Nil(t, peer)
}

func TestRouteSubmitForwardsFromNonSubmitter(t *testing.T) {
	rpc := &mockClusterRPC{}
	chain := &Chain{
		channelID:            "ch",
		selfConsenterID:      2,
		submitterConsenterID: 1,
		submitterPeer:        &peerAdapter{rpc: rpc, destination: 1},
		submitForwardTimeout: time.Second,
		submitC:              make(chan *submitReq, 1),
		configC:              make(chan *submitReq, 1),
		haltC:                make(chan struct{}),
	}

	env := &common.Envelope{Payload: []byte("tx")}
	err := chain.routeSubmit(&orderer.SubmitRequest{Channel: "ch", LastValidationSeq: 9, Payload: env}, false)

	require.NoError(t, err)
	require.Len(t, rpc.submitCalls, 1)
	require.EqualValues(t, 1, rpc.submitCalls[0].Destination)
	require.Equal(t, "ch", rpc.submitCalls[0].Channel)
	require.Equal(t, []byte("tx"), rpc.submitCalls[0].Payload)
	require.Empty(t, chain.submitC)
}

func TestRouteSubmitEnqueuesOnSubmitter(t *testing.T) {
	chain := &Chain{
		channelID:            "ch",
		selfConsenterID:      1,
		submitterConsenterID: 1,
		submitC:              make(chan *submitReq, 1),
		configC:              make(chan *submitReq, 1),
		haltC:                make(chan struct{}),
	}

	env := &common.Envelope{Payload: []byte("tx")}
	err := chain.routeSubmit(&orderer.SubmitRequest{Channel: "ch", LastValidationSeq: 9, Payload: env}, false)

	require.NoError(t, err)
	select {
	case req := <-chain.submitC:
		require.Same(t, env, req.env)
		require.EqualValues(t, 9, req.configSeq)
	default:
		t.Fatal("expected submitter to enqueue local submit")
	}
}

func TestRouteSubmitFallsBackToSelfWhenPreferredSubmitterFails(t *testing.T) {
	rpc := &mockClusterRPC{err: errors.New("unreachable")}
	chain := &Chain{
		channelID:            "ch",
		selfConsenterID:      2,
		submitterCandidates:  []submitterTarget{{id: 1, peer: &peerAdapter{rpc: rpc, destination: 1}}, {id: 2}},
		submitForwardTimeout: time.Second,
		submitC:              make(chan *submitReq, 1),
		configC:              make(chan *submitReq, 1),
		haltC:                make(chan struct{}),
	}

	env := &common.Envelope{Payload: []byte("tx")}
	err := chain.routeSubmit(&orderer.SubmitRequest{Channel: "ch", LastValidationSeq: 9, Payload: env}, false)

	require.NoError(t, err)
	require.Len(t, rpc.submitCalls, 1)
	select {
	case req := <-chain.submitC:
		require.Same(t, env, req.env)
		require.EqualValues(t, 9, req.configSeq)
	default:
		t.Fatal("expected fallback submitter to enqueue local submit")
	}
}

func TestRetryBatchForLosingProposalKeepsMissingEnvelopes(t *testing.T) {
	envA := &common.Envelope{Payload: []byte("a")}
	envB := &common.Envelope{Payload: []byte("b")}
	envC := &common.Envelope{Payload: []byte("c")}
	chain := &Chain{}
	chain.rememberInflightProposal(7, []*common.Envelope{envA, envB, envC}, []byte("local-state"))

	decidedBlock := testBlockWithEnvelopes(t, 7, envB)
	retry := chain.retryBatchForLosingProposal([]byte("winning-state"), decidedBlock)

	require.Equal(t, []*common.Envelope{envA, envC}, retry)
}

func TestRetryBatchForWinningProposalDoesNothing(t *testing.T) {
	env := &common.Envelope{Payload: []byte("a")}
	chain := &Chain{}
	chain.rememberInflightProposal(7, []*common.Envelope{env}, []byte("local-state"))

	retry := chain.retryBatchForLosingProposal([]byte("local-state"), testBlockWithEnvelopes(t, 7))

	require.Nil(t, retry)
}

func TestRetryBatchForLosingProposalHandlesDuplicateEnvelopes(t *testing.T) {
	env := &common.Envelope{Payload: []byte("duplicate")}
	chain := &Chain{}
	chain.rememberInflightProposal(7, []*common.Envelope{env, env}, []byte("local-state"))

	decidedBlock := testBlockWithEnvelopes(t, 7, env)
	retry := chain.retryBatchForLosingProposal([]byte("winning-state"), decidedBlock)

	require.Equal(t, []*common.Envelope{env}, retry)
}

func metadataSignatureForID(t *testing.T, id uint32) *common.MetadataSignature {
	t.Helper()
	identifierHeader, err := proto.Marshal(&common.IdentifierHeader{Identifier: id})
	require.NoError(t, err)
	return &common.MetadataSignature{
		IdentifierHeader: identifierHeader,
		Signature:        []byte{byte(id)},
	}
}

func blockProposalPayload(blockNumber uint64, hash [sha256.Size]byte, blockBytes []byte) []byte {
	payload := make([]byte, len(blockProposalMessageMagic)+8+sha256.Size+len(blockBytes))
	copy(payload, blockProposalMessageMagic)
	offset := len(blockProposalMessageMagic)
	binary.BigEndian.PutUint64(payload[offset:], blockNumber)
	offset += 8
	copy(payload[offset:], hash[:])
	offset += sha256.Size
	copy(payload[offset:], blockBytes)
	return payload
}

func testBlockWithEnvelopes(t *testing.T, number uint64, envs ...*common.Envelope) *common.Block {
	t.Helper()
	return testBlockWithPreviousHashAndEnvelopes(t, number, []byte("previous"), envs...)
}

func testBlockWithPreviousHashAndEnvelopes(t *testing.T, number uint64, previousHash []byte, envs ...*common.Envelope) *common.Block {
	t.Helper()

	data := &common.BlockData{Data: make([][]byte, len(envs))}
	for i, env := range envs {
		raw, err := proto.Marshal(env)
		require.NoError(t, err)
		data.Data[i] = raw
	}
	block := protoutil.NewBlock(number, previousHash)
	block.Data = data
	block.Header.DataHash = protoutil.ComputeBlockDataHash(data)
	return block
}

func testLazyBDLSConsensus(t *testing.T, callback func(*bdlslib.Message, *bdlslib.SignedProto)) *bdlslib.Consensus {
	t.Helper()

	keys := make([]*ecdsa.PrivateKey, 4)
	participants := make([]bdlslib.Identity, 4)
	for i := range keys {
		key, err := ecdsa.GenerateKey(bdlslib.S256Curve, rand.Reader)
		require.NoError(t, err)
		keys[i] = key
		participants[i] = bdlslib.DefaultPubKeyToIdentity(&key.PublicKey)
	}
	consensus, err := bdlslib.NewConsensus(&bdlslib.Config{
		Epoch:        time.Now(),
		PrivateKey:   keys[0],
		Participants: participants,
		StateCompare: func(a, b bdlslib.State) int {
			return bytes.Compare(a, b)
		},
		StateValidate: func(bdlslib.State) bool {
			return true
		},
		LazyStart:          true,
		MessageOutCallback: callback,
	})
	require.NoError(t, err)
	return consensus
}
