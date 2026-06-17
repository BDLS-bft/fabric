/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
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

func metadataSignatureForID(t *testing.T, id uint32) *common.MetadataSignature {
	t.Helper()
	identifierHeader, err := proto.Marshal(&common.IdentifierHeader{Identifier: id})
	require.NoError(t, err)
	return &common.MetadataSignature{
		IdentifierHeader: identifierHeader,
		Signature:        []byte{byte(id)},
	}
}
