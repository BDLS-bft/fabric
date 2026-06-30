/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	bdlslib "github.com/BDLS-bft/bdls"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"go.uber.org/zap/zapcore"
)

type bdlsTrace struct {
	logger *flogging.FabricLogger
	forced bool

	mu       sync.Mutex
	inbound  map[string]bdlsTraceBucket
	outbound map[string]bdlsTraceBucket
}

type bdlsTraceBucket struct {
	count        int64
	signedBytes  int64
	messageBytes int64
	stateBytes   int64
	proofCount   int64
	proofBytes   int64
}

func newBDLSTrace(logger *flogging.FabricLogger) *bdlsTrace {
	return &bdlsTrace{
		logger: logger,
		forced: bdlsTraceForced(),
	}
}

func (t *bdlsTrace) enabled() bool {
	return t != nil && t.logger != nil && (t.forced || t.debugEnabled())
}

func (t *bdlsTrace) debugEnabled() bool {
	return t != nil && t.logger != nil && t.logger.IsEnabledFor(zapcore.DebugLevel)
}

func bdlsTraceForced() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FABRIC_BDLS_TRACE_DECISIONS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return strings.Contains(os.Getenv("FABRIC_LOGGING_SPEC"), "orderer.consensus.bdls.trace")
}

func (t *bdlsTrace) recordInbound(payload []byte) {
	if !t.enabled() {
		return
	}

	signed, err := bdlslib.DecodeSignedMessage(payload)
	if err != nil {
		t.recordDecodeError("inbound", err, len(payload))
		return
	}
	m, err := bdlslib.DecodeMessage(signed.Message)
	if err != nil {
		t.recordDecodeError("inbound", err, len(payload))
		return
	}
	t.record("inbound", m, signed, len(payload))
}

func (t *bdlsTrace) recordOutbound(m *bdlslib.Message, signed *bdlslib.SignedProto) {
	if !t.enabled() || m == nil {
		return
	}
	t.record("outbound", m, signed, signedSize(signed))
}

func (t *bdlsTrace) record(direction string, m *bdlslib.Message, signed *bdlslib.SignedProto, signedBytes int) {
	bucket := bdlsTraceBucket{
		count:        1,
		signedBytes:  int64(signedBytes),
		messageBytes: int64(m.Size()),
		stateBytes:   int64(len(m.State)),
		proofCount:   int64(len(m.Proof)),
		proofBytes:   int64(proofBytes(m.Proof)),
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	switch direction {
	case "inbound":
		t.inbound = addTraceBucket(t.inbound, m.Type.String(), bucket)
	case "outbound":
		t.outbound = addTraceBucket(t.outbound, m.Type.String(), bucket)
	}
}

func (t *bdlsTrace) recordDecodeError(direction string, err error, payloadBytes int) {
	if !t.enabled() {
		return
	}
	bucket := bdlsTraceBucket{count: 1, signedBytes: int64(payloadBytes)}
	if err != nil {
		bucket.messageBytes = int64(len(err.Error()))
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	switch direction {
	case "inbound":
		t.inbound = addTraceBucket(t.inbound, "decode_error", bucket)
	case "outbound":
		t.outbound = addTraceBucket(t.outbound, "decode_error", bucket)
	}
}

func (t *bdlsTrace) logDecision(
	blockNumber uint64,
	height uint64,
	round uint64,
	envelopeCount int,
	consensusFinality time.Duration,
	commitPipeline time.Duration,
	blockSignature time.Duration,
	ledgerWrite time.Duration,
) {
	if !t.enabled() {
		return
	}

	inbound, outbound := t.snapshotAndReset()
	format := "bdls trace decision block=%d height=%d round=%d envelopes=%d consensus_finality=%s commit_pipeline=%s block_signature=%s ledger_write=%s inbound={%s} outbound={%s}"
	args := []interface{}{
		blockNumber,
		height,
		round,
		envelopeCount,
		consensusFinality,
		commitPipeline,
		blockSignature,
		ledgerWrite,
		formatTraceBuckets(inbound),
		formatTraceBuckets(outbound),
	}
	if t.debugEnabled() {
		t.logger.Debugf(format, args...)
		return
	}
	if t.forced {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

func (t *bdlsTrace) snapshotAndReset() (map[string]bdlsTraceBucket, map[string]bdlsTraceBucket) {
	t.mu.Lock()
	defer t.mu.Unlock()

	inbound := cloneTraceBuckets(t.inbound)
	outbound := cloneTraceBuckets(t.outbound)
	t.inbound = nil
	t.outbound = nil
	return inbound, outbound
}

func addTraceBucket(m map[string]bdlsTraceBucket, key string, delta bdlsTraceBucket) map[string]bdlsTraceBucket {
	if m == nil {
		m = make(map[string]bdlsTraceBucket)
	}
	cur := m[key]
	cur.count += delta.count
	cur.signedBytes += delta.signedBytes
	cur.messageBytes += delta.messageBytes
	cur.stateBytes += delta.stateBytes
	cur.proofCount += delta.proofCount
	cur.proofBytes += delta.proofBytes
	m[key] = cur
	return m
}

func cloneTraceBuckets(src map[string]bdlsTraceBucket) map[string]bdlsTraceBucket {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]bdlsTraceBucket, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func formatTraceBuckets(buckets map[string]bdlsTraceBucket) string {
	if len(buckets) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		b := buckets[key]
		parts = append(parts, fmt.Sprintf(
			"%s count=%d signed_bytes=%d message_bytes=%d state_bytes=%d proof_count=%d proof_bytes=%d",
			key,
			b.count,
			b.signedBytes,
			b.messageBytes,
			b.stateBytes,
			b.proofCount,
			b.proofBytes,
		))
	}
	return strings.Join(parts, "; ")
}

func signedSize(signed *bdlslib.SignedProto) int {
	if signed == nil {
		return 0
	}
	return signed.Size()
}

func proofBytes(proofs []*bdlslib.SignedProto) int {
	total := 0
	for _, proof := range proofs {
		total += signedSize(proof)
	}
	return total
}
