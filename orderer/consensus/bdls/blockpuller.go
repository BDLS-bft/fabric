/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"crypto/x509"
	"encoding/pem"

	"github.com/hyperledger/fabric-lib-go/bccsp"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/orderer/common/cluster"
	"github.com/hyperledger/fabric/orderer/common/localconfig"
	"github.com/hyperledger/fabric/orderer/consensus"
	"github.com/pkg/errors"
)

// BlockPuller is the minimal surface the BDLS chain run-loop needs for
// catch-up. It's intentionally the same shape etcdraft and smartbft use so
// chain.go can hold a vanilla cluster.BlockPuller or a mock interchangeably
// during tests.
type BlockPuller interface {
	PullBlock(seq uint64) *common.Block
	HeightsByEndpoints() (map[string]uint64, string, error)
	Close()
}

// CreateBlockPuller is the factory chain.go invokes lazily — building a
// puller takes a TLS dial and pulls a config block, so we avoid doing it at
// HandleChain time and pay the cost the first time we actually need to
// fetch.
type CreateBlockPuller func() (BlockPuller, error)

// LedgerBlockPuller is the "look in the ledger first, fall back to the
// network" wrapper. Every BDLS catch-up path starts here: if the block the
// chain asks for is already on disk we return it immediately (the common
// case during leader re-synchronisation inside a quorum), otherwise we
// punt to the cluster BlockPuller for the real network fetch.
//
// Ported with no changes from etcdraft's LedgerBlockPuller — both
// consenters need the same ledger-first semantics, and forking the
// implementation just to rename it would be pointless duplication.
type LedgerBlockPuller struct {
	BlockPuller
	BlockRetriever cluster.BlockRetriever
	Height         func() uint64
}

func (lp *LedgerBlockPuller) PullBlock(seq uint64) *common.Block {
	lastSeq := lp.Height() - 1
	if lastSeq >= seq {
		return lp.BlockRetriever.Block(seq)
	}
	return lp.BlockPuller.PullBlock(seq)
}

// NewBlockPuller constructs a ledger-first, network-fallback puller for the
// given channel. The cluster puller it wraps handles the actual fetch via
// the existing orderer TLS dialer, so BDLS does not introduce any new
// transport. Catch-up inherits the same BFT block-sequence verifier
// smartbft uses (cluster.VerifyBlocksBFT + support.SignatureVerifier) —
// BDLS blocks carry a Fabric-standard signature set in their
// BlockMetadata[SIGNATURES] slot, and the BDLS <decide> proof lives in
// BlockMetadata[ORDERER] for external verification, not inside the block
// header.
func NewBlockPuller(
	support consensus.ConsenterSupport,
	baseDialer *cluster.PredicateDialer,
	clusterConfig localconfig.Cluster,
	csp bccsp.BCCSP,
) (BlockPuller, error) {
	verifyBlockSequence := func(blocks []*common.Block, _ string) error {
		vb := cluster.BlockVerifierBuilder(csp)
		return cluster.VerifyBlocksBFT(blocks, support.SignatureVerifier(), vb)
	}

	stdDialer := &cluster.StandardDialer{
		Config: baseDialer.Config,
	}
	stdDialer.Config.AsyncConnect = false
	stdDialer.Config.SecOpts.VerifyCertificate = nil

	// Extract TLS CA certs and endpoints from the channel's last config
	// block. A fresh cluster.BlockPuller cannot dial anything without
	// this data, so we fail HandleChain-time rather than at the first
	// catch-up attempt.
	endpoints, err := endpointconfigFromSupport(support, csp)
	if err != nil {
		return nil, err
	}

	der, _ := pem.Decode(stdDialer.Config.SecOpts.Certificate)
	if der == nil {
		return nil, errors.Errorf("bdls blockpuller: orderer client certificate is not valid PEM: %v",
			string(stdDialer.Config.SecOpts.Certificate))
	}

	logger := flogging.MustGetLogger("orderer.consensus.bdls.puller").With("channel", support.ChannelID())

	myCert, err := x509.ParseCertificate(der.Bytes)
	if err != nil {
		// Non-fatal — cluster.BlockPuller just uses this to avoid dialing
		// its own endpoint. A missing/malformed self-cert means we may
		// occasionally do so; we log once and move on.
		logger.Warnf("bdls blockpuller: failed to parse own TLS cert (%v); catch-up may briefly dial self", err)
	}

	bp := &cluster.BlockPuller{
		MyOwnTLSCert:        myCert,
		VerifyBlockSequence: verifyBlockSequence,
		Logger:              logger,
		RetryTimeout:        clusterConfig.ReplicationRetryTimeout,
		MaxTotalBufferBytes: clusterConfig.ReplicationBufferSize,
		FetchTimeout:        clusterConfig.ReplicationPullTimeout,
		Endpoints:           endpoints,
		Signer:              support,
		TLSCert:             der.Bytes,
		Channel:             support.ChannelID(),
		Dialer:              stdDialer,
		StopChannel:         make(chan struct{}),
	}

	return &LedgerBlockPuller{
		Height:         support.Height,
		BlockRetriever: support,
		BlockPuller:    bp,
	}, nil
}

// endpointconfigFromSupport resolves the last config block through the
// supplied ConsenterSupport and extracts the cluster endpoints/CAs the
// puller will dial. Factored out so unit tests can plug a different
// ConsenterSupport without rewiring the whole puller constructor.
func endpointconfigFromSupport(support consensus.ConsenterSupport, csp bccsp.BCCSP) ([]cluster.EndpointCriteria, error) {
	lastConfigBlock, err := lastConfigBlockFromSupport(support)
	if err != nil {
		return nil, err
	}
	return cluster.EndpointconfigFromConfigBlock(lastConfigBlock, csp)
}

func lastConfigBlockFromSupport(support consensus.ConsenterSupport) (*common.Block, error) {
	lastBlockSeq := support.Height() - 1
	lastBlock := support.Block(lastBlockSeq)
	if lastBlock == nil {
		return nil, errors.Errorf("bdls blockpuller: unable to retrieve block [%d] from ledger", lastBlockSeq)
	}
	lastConfigBlock, err := cluster.LastConfigBlock(lastBlock, support)
	if err != nil {
		return nil, err
	}
	return lastConfigBlock, nil
}
