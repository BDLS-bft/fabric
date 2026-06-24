# BDLS performance baseline and research plan

This page records the initial BDLS ordering-service benchmark baseline used by
the integration roadmap. The numbers are local smoke measurements, not a
production capacity claim. They are intended to verify that the benchmark
harness works, expose the relative ordering-service behavior under identical
test-network conditions, and define the matrix that should be expanded before
making deployment recommendations.

The benchmark plan is derived from the BDLS papers referenced by this work:

* Wang, "Byzantine Fault Tolerance in Partial Synchronous Networks"
* Al Salih and Wang, "BDLS as a Blockchain Finality Gadget: Improving
  Byzantine Fault Tolerance in Hyperledger Fabric"

The central claim to test is not single-transaction latency. The papers argue
that BDLS uses a star-shaped fast path with linear communication complexity:
`4n` messages in the honest-leader synchronized case, compared with
PBFT-family `2n^2 + n` communication. The benchmark matrix therefore needs to
stress filled blocks and larger orderer sets, where message complexity should
matter.

Two caveats are important when comparing these local measurements with the
papers. First, the original BDLS paper describes linear
communication/authenticator complexity when threshold signatures are used. This
Fabric integration still carries individual ECDSA proof bundles in several
paths, so the implementation should not be expected to match the paper's
asymptotic authenticator cost yet. Second, the Fabric BDLS finality-gadget paper
reports TPS by dividing a large transaction count by the time until the last
transaction is written. A one-block local benchmark can report a very high
burst value when both client send time and final commit wait happen to be low;
that is regression evidence, not a sustained throughput claim.

## Benchmark harness

Run the benchmark from the repository root:

```bash
BENCH_MODE=broadcast scripts/run-bdls-benchmark-sweep.sh smoke
```

The default `broadcast` mode measures direct ordering-service throughput with
signed endorser-transaction envelopes. It avoids peer lifecycle, endorsement,
and chaincode execution so that the reported `tx/s` primarily reflects the
ordering path. The benchmark deliver client raises its gRPC receive limit from
the default 4 MB to a value derived from `AbsoluteMaxBytes`, so tests with
larger preferred block sizes can actually deliver the measured blocks. It also
reports attribution metrics:

* `send_tx/s` and `send_s`: client broadcast send/ack rate and duration.
* `commit_wait_s`: time spent waiting for the ordering service to deliver all
  submitted envelopes after broadcast acknowledgements return.
* `blocks`, `tx/block`, and `block/s`: committed block count, average fill,
  and block commit rate for the measured sample.

The legacy `e2e` mode is still available for full peer and chaincode coverage:

```bash
BENCH_MODE=e2e scripts/run-bdls-benchmark-sweep.sh smoke
```

Useful knobs:

* `BENCH_MODE=broadcast|e2e`
* `BENCH_N=<transactions>`
* `BENCH_REPEATS=<samples>`
* `BENCH_WARMUP_REPEATS=<samples>` (default 0) runs unmeasured warm-up benchmark samples
  to reduce startup jitter before collecting metrics
* `BENCH_BDLS_RELIABLE_DECIDE=true|false` (default true) toggles BDLS `reliable_decide` path in benchmark runs
* `BENCH_BDLS_COMPACT_STATE=true|false` (default true) toggles BDLS compact block state proposals
* `BENCH_CONCURRENCY=<workers>`
* `BENCH_BDLS_LATENCY_FAST=<duration>` and
  `BENCH_BDLS_DELTA*_FAST=<duration>` for the local-fast tuning case
* `-bdls.bench.consensus=BDLS|BFT|etcdraft|all`
* `-bdls.bench.batch-timeout=1s`
* `-bdls.bench.max-message-count=500`
* `-bdls.bench.absolute-max-bytes-mb=10`
* `-bdls.bench.preferred-max-bytes-kb=512`
* `-bdls.bench.payload-bytes=64`
* `-bdls.bench.latency=100ms`
* `-bdls.bench.delta0`, `-bdls.bench.delta1`,
  `-bdls.bench.delta-prime1`, `-bdls.bench.delta2`, and
  `-bdls.bench.delta3`

## Direct ordering baseline

Environment: local NWO integration network on a developer machine,
direct-broadcast benchmark mode, one peer per org, 1-byte payloads, and
`BENCH_BDLS_LATENCY=100ms` for BDLS.

| Case | Consensus | Orderers | Max message count | Benchmark N | Concurrency | Result |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Under-filled block smoke | BDLS | 4 | 100 | 25 | 8 | 4.624 tx/s |
| Under-filled block smoke | SmartBFT (`BFT`) | 4 | 100 | 25 | 8 | 11.89 tx/s |
| Under-filled block smoke | etcdraft | 3 | 100 | 25 | 8 | 23.93 tx/s |
| Filled 100-tx block | BDLS | 4 | 100 | 100 | 16 | 44.93 tx/s |
| Filled 100-tx block | SmartBFT (`BFT`) | 4 | 100 | 100 | 16 | 48.07 tx/s |
| Filled 100-tx block | etcdraft | 3 | 100 | 100 | 16 | 1982 tx/s |
| Filled 1000-tx block | BDLS | 4 | 1000 | 1000 | 32 | 323.9 tx/s |
| Filled 1000-tx block, local-fast delta | BDLS | 4 | 1000 | 1000 | 32 | 513.4 tx/s |
| Filled 1000-tx block, local-fast delta, 2 MB preferred block | BDLS | 4 | 1000 | 1000 | 32 | 2309 tx/s |
| Filled 1000-tx block, 2 MB preferred block | SmartBFT (`BFT`) | 4 | 1000 | 1000 | 32 | 311.4 tx/s |
| Filled 1000-tx block, 2 MB preferred block | etcdraft | 3 | 1000 | 1000 | 32 | 10717 tx/s |
| Filled 1000-tx block | SmartBFT (`BFT`) | 4 | 1000 | 1000 | 32 | 321.3 tx/s |
| Filled 1000-tx block | etcdraft | 3 | 1000 | 1000 | 32 | 934.3 tx/s |
| Filled 1000-tx block | BDLS | 7 | 1000 | 1000 | 32 | 321.7 tx/s |
| Filled 1000-tx block | SmartBFT (`BFT`) | 7 | 1000 | 1000 | 32 | 305.8 tx/s |
| Filled 1000-tx block | etcdraft | 7 | 1000 | 1000 | 32 | 922.3 tx/s |

## Focused local sweep

The `focused` sweep compares the same filled 1000-transaction, 2 MB preferred
block shape across BDLS, SmartBFT, and etcdraft, and emits a TSV summary next
to the raw benchmark log:

```bash
BENCH_MODE=broadcast BENCH_OUT_DIR=_benchmarks/bdls/focused \
    scripts/run-bdls-benchmark-sweep.sh focused
```

Set `BENCH_REPEATS` when comparing consensus implementations. Each case is run
as an independent `go test -bench` process, producing:

* `*.summary.tsv`: one row per measured sample.
* `*.aggregate.tsv`: mean, min, max, and standard deviation by case.

Sample from 2026-06-17:

| Label | Consensus | Orderers | tx/s | send tx/s | Commit wait | Blocks | tx/block | block/s |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| focused-local-default | BDLS | 4 | 1001 | 15665 | 0.9353s | 1 | 1000 | 1.001 |
| focused-local-fast | BDLS | 4 | 2195 | 17199 | 0.3975s | 1 | 1000 | 2.195 |
| focused-local-fast | BDLS | 7 | 463.8 | 11929 | 2.072s | 1 | 1000 | 0.4638 |
| focused-local | SmartBFT (`BFT`) | 4 | 310.8 | 18264 | 3.163s | 3 | 333.3 | 0.9323 |
| focused-local | SmartBFT (`BFT`) | 7 | 303.6 | 17478 | 3.237s | 3 | 333.3 | 0.9107 |
| focused-local | etcdraft | 3 | 9246 | 17527 | 0.05109s | 1 | 1000 | 9.246 |
| focused-local | etcdraft | 7 | 6313 | 16912 | 0.09927s | 1 | 1000 | 6.313 |

Several exploratory runs preceded the sustained sweep. They exposed three
important measurement hazards: one-block burst results could report unrealistic
TPS, aggressive BDLS timeout settings could trigger round churn, and early
larger-node runs accidentally disabled compact block state. Those data points
are useful for debugging, but the comparison below supersedes them for PR
review.

## Sustained 50-block sweep

The sustained sweep submits 50,000 envelopes with `MaxMessageCount=1000`,
which forces 50 full blocks per case. It uses one peer per org, 1-byte payloads,
2 MB preferred blocks, concurrency 64, direct ordering-service broadcast mode,
and compact BDLS state. BDLS uses the balanced local schedule
`LatencyMs=100`, `Delta0=50ms`, `Delta1=100ms`, `DeltaPrime1=50ms`,
`Delta2=50ms`, and `Delta3=50ms`.

Command:

```bash
BENCH_N=50000 BENCH_REPEATS=10 BENCH_CONCURRENCY=64 \
    BENCH_PREFERRED_MAX_BYTES_KB=2048 BENCH_PAYLOAD_BYTES=1 \
    BENCH_BATCH_TIMEOUT=1s BENCH_COMMIT_TIMEOUT=5m \
    BENCH_BDLS_LATENCY_SUSTAINED=100ms \
    BENCH_BDLS_DELTA0_SUSTAINED=50ms \
    BENCH_BDLS_DELTA1_SUSTAINED=100ms \
    BENCH_BDLS_DELTA_PRIME1_SUSTAINED=50ms \
    BENCH_BDLS_DELTA2_SUSTAINED=50ms \
    BENCH_BDLS_DELTA3_SUSTAINED=50ms \
    scripts/run-bdls-benchmark-sweep.sh sustained
```

Ten-sample aggregate from 2026-06-19:

| Consensus | Orderers | Samples | Mean tx/s | Min | Max | Stddev | CV | Mean send tx/s | Mean commit wait | Quality |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| BDLS | 7 | 10 | 3213 | 2007 | 4266 | 959 | 29.8% | 3264 | 0.2269s | high variance |
| BDLS | 13 | 10 | 3187 | 1757 | 3644 | 665 | 20.9% | 3239 | 0.2445s | high variance |
| BDLS | 25 | 10 | 1676 | 1130 | 2060 | 269 | 16.1% | 1712 | 0.6681s | ok |
| SmartBFT (`BFT`) | 7 | 10 | 1844 | 1354 | 2453 | 388 | 21.0% | 1878 | 0.5357s | high variance |
| SmartBFT (`BFT`) | 13 | 10 | 1668 | 636.7 | 2364 | 525 | 31.5% | 1724 | 0.8541s | high variance |
| SmartBFT (`BFT`) | 25 | 10 | 781.1 | 464.1 | 1065 | 174 | 22.3% | 807.6 | 1.9921s | high variance |
| etcdraft | 7 | 10 | 3037 | 1906 | 4000 | 780 | 25.7% | 3078 | 0.2092s | high variance |
| etcdraft | 13 | 10 | 1985 | 1153 | 2701 | 462 | 23.3% | 2015 | 0.3950s | high variance |
| etcdraft | 25 | 10 | 2535 | 1858 | 3144 | 370 | 14.6% | 2593 | 0.4711s | ok |

The raw benchmark log, generated aggregate TSV, and per-sample summary were
generated under `_benchmarks/` and are intentionally not checked in because the
raw log is large and machine-local. The aggregate values are recorded in the
table above.

This is still a local NWO benchmark, not a production capacity claim. It is
substantially more reliable than the earlier one-block and five-block runs
because every sample commits the same 50 full blocks and each data point is
averaged across 10 sequential runs.

The sustained curve is directionally coherent but still noisy. BDLS is ahead of
SmartBFT at all three orderer counts and is roughly 2.1x SmartBFT at 25
orderers in this harness. That matches the expected communication-complexity
advantage better than the earlier burst table. It is not a clean linear-scaling
result: BDLS/25 drops to about 52% of BDLS/13, and the 7- and 13-orderer BDLS
points have high coefficient of variation. BDLS also does not generally beat
CFT raft in this direct ordering-service benchmark, which is expected because
raft does not pay BFT proof, verification, and Fabric metadata-signature costs.

## What the current data proves

The sustained sweep proves a narrower claim: in this local direct-ordering
harness, with compact BDLS state, 50 full blocks per sample, and matched
1000-envelope batching, BDLS outperforms SmartBFT at 7, 13, and 25 orderers.
It does not yet prove that the pure BDLS consensus library is faster than
SmartBFT, nor does it prove production linear scaling. The reported `tx/s`
includes client broadcast acknowledgements, orderer ingress backpressure,
post-decision Fabric BFT block-signature collection, ledger writes, and deliver
reads in addition to the BDLS decision itself.

The production metrics now expose the required split:

* `consensus_bdls_consensus_finality_duration`: local proposal submission to
  matching BDLS decision.
* `consensus_bdls_block_signature_duration`: post-decision Fabric BFT metadata
  signature quorum exchange.
* `consensus_bdls_ledger_write_duration`: local ledger persistence.
* `consensus_bdls_commit_pipeline_duration`: full post-decision commit path.

For debug-log based studies, enable
`orderer.consensus.bdls=debug:orderer.consensus.bdls.trace=debug`, or use the
benchmark-only `FABRIC_BDLS_TRACE_DECISIONS=true` switch, and parse the orderer
logs with:

```bash
scripts/parse-bdls-trace.py _benchmarks/**/orderer*.log > bdls_trace.tsv
scripts/parse-bdls-trace.py --aggregate _benchmarks/**/orderer*.log > bdls_trace_aggregate.tsv
```

The sweep script also includes a trace mode that runs the sustained BDLS
7/13/25-orderer shape with decision tracing enabled and writes both parser
outputs next to the normal benchmark summary:

```bash
BENCH_N=50000 BENCH_REPEATS=1 BENCH_CONCURRENCY=64 \
    BENCH_PREFERRED_MAX_BYTES_KB=2048 BENCH_PAYLOAD_BYTES=1 \
    scripts/run-bdls-benchmark-sweep.sh trace
```

The trace mode emits:

* `*.summary.tsv`: throughput and commit-wait summary.
* `*.aggregate.tsv`: throughput aggregate by node count.
* `*.metrics/`: per-orderer Prometheus snapshots collected from the
  authenticated operations endpoints after each measured benchmark sample.
* `*.metrics.tsv`: per-case consensus-stage histogram summary aggregated from
  those orderer snapshots.
* `*.bdls_trace.tsv`: per-block BDLS finality/signature/ledger trace rows,
  tagged with the benchmark label, orderer count, sample, and run type.
* `*.bdls_trace_aggregate.tsv`: per-case finality/signature/ledger and
  message/proof-byte aggregate rows.

Trace mode was exercised again on 2026-06-19 after the harness fixes. The
usable run submitted 5000 envelopes per case with 1000 tx/block, 64-way client
concurrency, compact state, reliable decide, and 1-byte payloads. The trace
aggregate produced this bottleneck classification:

| Orderers | Mean tx/s | Mean send tx/s | Commit wait mean | Consensus finality mean | Signature quorum mean | Ledger write mean | Commit pipeline mean | Classification |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| 7 | 4106 | 6476 | 0.4455s | 15.0ms | 16.6ms | 54.5ms | 73.8ms | baseline |
| 13 | 2469 | 5498 | 1.116s | 17.4ms | 6.23ms | 80.0ms | 89.4ms | mixed / unattributed |
| 25 | 1277 | 4696 | 2.850s | 20.2ms | 33.8ms | 81.3ms | 136.5ms | post-decision signature quorum |

The 25-orderer slowdown is not primarily explained by BDLS finality in this
run: trace finality grows only about 16% from 13 to 25 orderers, while the
post-decision Fabric block-signature quorum grows about 5.4x and inbound proof
bytes grow from roughly 26.7 KB/block to 101.4 KB/block. That points to Fabric
integration overhead after BDLS decides the block, especially proof/signature
handling and commit-pipeline cost, as the immediate 25-node bottleneck. The
13-orderer drop is less cleanly attributed because send throughput drops,
signature time falls, and ledger/pipeline costs rise moderately; treat it as
mixed local backpressure and integration-pipeline behavior rather than proof of
a BDLS protocol limit.

Join the throughput and trace aggregates into a bottleneck classification with:

```bash
trace_run=_benchmarks/bdls/trace-YYYYMMDD-HHMMSS
scripts/analyze-bdls-scaling.py \
    --throughput "${trace_run}.aggregate.tsv" \
    --trace "${trace_run}.bdls_trace_aggregate.tsv"
```

The analyzer reports throughput drop, finality growth, signature-quorum growth,
and a conservative `likely_bottleneck` classification for each larger orderer
count. When multiple measured rows exist for the same orderer count, throughput
rows are weighted by sample count and trace rows are weighted by decided block
count. Treat `mixed_or_unattributed` as a signal to inspect CPU, scheduler,
network, and client ingress data before making a linear-scaling claim.

The generated TSV records finality, post-decision signature quorum, ledger
write, and inbound/outbound BDLS message/proof counters per decided block. This
is the evidence needed to decide whether the 25-orderer slowdown is a BDLS
consensus limit or Fabric integration overhead. The aggregate form reports
mean, p50, p95, and max finality/signature timings plus signature-to-finality
ratio, grouped by benchmark case, which should be compared across the 7-, 13-,
and 25-orderer rows.

A pure consensus comparison should compare BDLS finality against equivalent
SmartBFT and raft consensus-stage measurements while keeping block fill,
payload size, client concurrency, and network shape identical. If BDLS finality
stays flat or degrades slowly from 7 to 25 orderers while
`block_signature_duration` or client send time grows, the bottleneck is in the
Fabric integration pipeline rather than the BDLS decision protocol. If finality
itself grows sharply at 25 orderers, the bottleneck is inside BDLS message
handling, proof verification, scheduler pressure, or compact-state recovery.

Existing cross-consensus observability is not perfectly symmetric:

* BDLS now exposes local proposal-to-decision finality plus post-decision
  signature, ledger, and commit-pipeline timings.
* SmartBFT now exposes `consensus_BFT_proposal_finality_duration`, a
  leader-local proposal assembly to matching delivered-decision histogram from
  the Fabric SmartBFT wrapper. This is the closest direct counterpart to BDLS
  local proposal finality for pure consensus-stage comparisons.
* SmartBFT exposes consensus latency metrics from the SmartBFT library, such as
  `consensus_smartbft_consensus_latency_sync`, plus request-pool and
  batch-processing histograms. The sync-latency histogram reports zero-valued
  samples in the current local runs, so request-pool and batch-processing
  metrics are more useful for diagnosing local SmartBFT pressure.
* etcdraft exposes raft leadership/proposal counters and
  `consensus_etcdraft_data_persist_duration`, but raft is crash-fault-tolerant
  and does not perform a BFT finality proof. Treat raft as a CFT throughput and
  persistence baseline, not as an equivalent BFT finality comparison.

The sweep script now captures authenticated operations metrics automatically.
For any sweep mode, the metrics summary is generated next to the throughput
summary:

```bash
BENCH_N=50000 BENCH_REPEATS=1 BENCH_CONCURRENCY=64 \
    BENCH_PREFERRED_MAX_BYTES_KB=2048 BENCH_PAYLOAD_BYTES=1 \
    scripts/run-bdls-benchmark-sweep.sh trace

scripts/compare-consensus-metrics.py \
    _benchmarks/bdls/trace-YYYYMMDD-HHMMSS.metrics/*.prom \
    > consensus_stage_metrics.tsv
```

The output table reports mean and p95 values for BDLS
finality/signature/ledger histograms, the SmartBFT wrapper
proposal-finality histogram, SmartBFT sync/pool/batch histograms, and raft
persistence latency. Use that table with the throughput aggregates; do not
compare raft persistence latency as though it were a BFT finality proof.

A 2026-06-20 local cross-stage run filled the remaining 7/13/25 metric rows
for SmartBFT and raft using 3000-envelope cases. Those runs are intentionally
short and startup-heavy, so they are diagnostic rather than a replacement for
the sustained 50-block throughput table. They show:

| Consensus | Orderers | Diagnostic tx/s | Primary stage metric | Mean | p95 |
| --- | ---: | ---: | --- | ---: | ---: |
| SmartBFT | 7 | 760.4 | batch processing | 55.7ms | 1s |
| SmartBFT | 13 | 439.7 | batch processing | 145.7ms | 1s |
| SmartBFT | 25 | 352.2 | batch processing | 298.3ms | 1s |
| raft | 7 | 3491 | data persistence | 17.1ms | 250ms |
| raft | 13 | 3318 | data persistence | 17.7ms | 100ms |
| raft | 25 | 1007 | data persistence | 61.0ms | 500ms |

SmartBFT request-pool latency also rises from 1.68s at 7 orderers to 3.42s at
13 and 4.73s at 25 in this short local run, showing substantial local queuing
pressure. This supports the ordering-service result that compact BDLS is ahead
of SmartBFT in the local harness, but it is not a symmetric pure-consensus proof
because the exported SmartBFT metrics do not expose the same
proposal-to-decision finality interval as the BDLS trace.

After adding the Fabric SmartBFT wrapper
`consensus_BFT_proposal_finality_duration` metric, a fresh 2026-06-20 matched
diagnostic run compared BDLS finality, SmartBFT proposal finality, and raft
persistence with the same 5000-envelope, 1000 tx/block, 64-way concurrency,
2 MB preferred block, and 1-byte payload shape:

| Consensus | Orderers | Diagnostic tx/s | Stage metric | Mean | p95 |
| --- | ---: | ---: | --- | ---: | ---: |
| BDLS | 7 | 4386 | proposal-to-decision finality | 138.7ms | 500ms |
| SmartBFT | 7 | 1038 | proposal-to-delivery finality | 259.4ms | 1s |
| raft | 7 | 6521 | data persistence | 15.9ms | 250ms |
| BDLS | 13 | 2929 | proposal-to-decision finality | 212.1ms | 500ms |
| SmartBFT | 13 | 581.8 | proposal-to-delivery finality | 730.8ms | 2.5s |
| raft | 13 | 3354 | data persistence | 16.4ms | 100ms |
| BDLS | 25 | 1503 | proposal-to-decision finality | 441.2ms | 1s |
| SmartBFT | 25 | 515.3 | proposal-to-delivery finality | 881.1ms | 2.5s |
| raft | 25 | 2046 | data persistence | 27.8ms | 250ms |

This is the first symmetric pure-BFT diagnostic in this branch: BDLS finality is
about 1.9x faster than SmartBFT at 7 orderers, 3.4x faster at 13 orderers, and
2.0x faster at 25 orderers for the measured mean. The p95 bucket comparison is
also better for BDLS at each orderer count. Treat this as proof-supporting
diagnostic evidence, not as final capacity guidance, because each point has one
measured sample and only five to six decided blocks. A release-quality
performance claim should repeat this shape for multiple samples and publish
mean/min/max/stdev/CV for the finality distributions.

## Proof and release gates

Treat the BDLS performance claim as three separate claims with separate
evidence requirements:

| Claim | Required evidence | Current status |
| --- | --- | --- |
| BDLS improves Fabric ordering throughput over SmartBFT | Same harness, same batch shape, same payload size, same client concurrency, multiple samples, and mean/min/max/stdev/CV reported for BDLS and SmartBFT | Supported by the 2026-06-19 sustained local direct-ordering sweep |
| BDLS pure consensus finality scales better than SmartBFT | Consensus-stage timing distributions, not end-to-end TPS alone, with finality p50/p95/max compared at the same orderer counts | Supported by the 2026-06-20 single-sample matched diagnostic; repeat with multiple samples before treating it as a production performance claim |
| BDLS is production releasable | Correct Fabric 3.x BFT block metadata signatures, deterministic block verification by peers, green CI, bounded retry behavior, observable commit stages, documented tuning knobs, and no reliance on machine-local benchmark artifacts | Integration path and observability are in place; final release should wait for fresh CI on the latest PR head and trace-backed scale attribution |

The release argument should therefore be:

* **Ordering-service throughput:** BDLS compact-state mode is better than
  SmartBFT in the current sustained local harness at 7, 13, and 25 orderers.
* **Pure consensus:** not proven by TPS alone. It requires
  `consensus_bdls_consensus_finality_duration`/trace data for BDLS and
  `consensus_BFT_proposal_finality_duration` data for SmartBFT under the same
  block shape. The 2026-06-20 matched diagnostic supports BDLS over SmartBFT on
  proposal-finality mean and p95 buckets at 7, 13, and 25 orderers. Current
  SmartBFT pool/batch metrics show local queuing and batch-processing pressure,
  but they should be treated as supporting diagnostics rather than the finality
  comparison itself.
* **Linear scaling:** not proven by current data. The 25-orderer BDLS point
  drops materially. The trace-backed diagnosis is that the 25-orderer loss is
  dominated by post-decision Fabric signature/proof and commit-pipeline
  overhead in this local run, not by BDLS finality alone.
* **Raft comparison:** raft is a CFT baseline, not a BFT competitor. BDLS need
  not beat raft TPS to be valuable; the defensible comparison is the cost paid
  for Byzantine safety versus the SmartBFT BFT baseline.

BDLS linear scaling is possible only for the protocol portion when the fast path
uses compact state and avoids quadratic authenticator growth. This integration
still carries individual signatures and performs a post-decision Fabric BFT
metadata-signature quorum exchange, so the full Fabric ordering service should
not be expected to scale linearly until those integration costs are either
pipelined, aggregated, or shown to be outside the critical path.

## Observations

* The early low BDLS values were dominated by under-filled blocks and
  `BatchTimeout`, not by steady-state BDLS consensus throughput.
* When compact block state is disabled, BDLS proof bundles carry full Fabric
  block bytes and scaling collapses at larger orderer counts. That
  non-compact mode is useful as a regression detector, but it is not the
  intended BDLS-over-Fabric performance path.
* With compact block state enabled, the 10-sample 50-block sweep is broadly
  consistent with the paper's communication-complexity argument: BDLS is
  materially ahead of SmartBFT at 7, 13, and 25 orderers. It is not yet a
  clean linear-scaling implementation result, especially at 25 orderers.
* Lowering the local BDLS schedule from `LatencyMs=100` with default delta
  values to `LatencyMs=20`, `Delta0=10ms`, `Delta1=20ms`,
  `DeltaPrime1=10ms`, `Delta2=10ms`, and `Delta3=10ms` improved the filled
  1000-transaction local result from 323.9 tx/s to 513.4 tx/s. That points to
  timeout configuration as a real throughput lever in localhost tests, but
  these values should not be treated as WAN-safe defaults.
* Increasing `PreferredMaxBytes` from 512 KB to 2 MB let BDLS commit the full
  1000-envelope sample as one block and improved the local-fast result to
  2309 tx/s in one run and 2195 tx/s in the focused sweep. The earlier 512 KB
  result split the same 1000 envelopes into two roughly 500-transaction blocks,
  so the low TPS was largely a block-size measurement artifact rather than a
  pure consensus limit.
* The sustained sweep shows a remaining BDLS scaling bottleneck at 25
  orderers: mean throughput drops from 3187 tx/s at 13 orderers to 1676 tx/s
  at 25 orderers while the client send rate drops in the same direction. In Fabric
  3.0 channels, BDLS also performs a post-decision BFT block-metadata
  signature quorum exchange before `WriteBlockSync`, so future measurements
  need to distinguish BDLS decision latency from Fabric block-signature quorum
  latency and broadcast-side backpressure.
* The benchmark now propagates the test's max-message-count, request-byte, and
  request-interval settings into the generated SmartBFT config. In the
  sustained sweep, SmartBFT produced 1000-transaction blocks for all three
  orderer counts, so the comparison is no longer distorted by the old
  hard-coded 100-request SmartBFT batch limit.
* etcdraft remains materially faster in direct ordering mode at 25 orderers
  because it is CFT, not BFT. The 7- and 13-orderer local points are noisy:
  raft is close to BDLS at 7 orderers and below BDLS at 13 orderers in this
  run, but the high variance means this should not be read as a general claim
  that BDLS beats raft.
* A normal compact-state retry path was previously logged at warning level when
  BDLS decided a compact block reference before the local full block bytes were
  available. The adapter now logs that expected retry at debug level. That
  avoids benchmark-distorting warning-log volume without hiding unexpected
  non-compact state resolution failures.
* During the focused sweep, stale BDLS round-change and decide messages were
  observed as repeated library sentinel errors. The Fabric adapter now keeps
  those expected stale-message returns at debug level so benchmark runs are not
  dominated by warning-log formatting, while unexpected `ReceiveMessage` errors
  remain warnings.

## Research-informed matrix

Use `scripts/run-bdls-benchmark-sweep.sh` for reproducible local sweeps:

* `smoke`: quick filled 100-transaction block comparison.
* `tuning`: compare 100-transaction and 1000-transaction filled blocks,
  including a serialized BDLS local-fast timeout case.
* `focused`: compare filled 1000-transaction, 2 MB preferred blocks across
  BDLS default, BDLS local-fast, SmartBFT, and etcdraft, and emit a TSV summary.
* `diagnostic`: isolate the current BDLS scaling question by running 4-orderer
  and 7-orderer BDLS cases at 500- and 1000-transaction block sizes under both
  default and local-fast timeout schedules.
* `sustained`: compare BDLS, SmartBFT, and etcdraft at 7, 13, and 25 orderers
  with 50,000 submitted envelopes, 1000-envelope blocks, three samples by
  default, and a balanced BDLS local schedule (`LatencyMs=100`,
  `Delta0=50ms`, `Delta1=100ms`, `DeltaPrime1=50ms`, `Delta2=50ms`,
  `Delta3=50ms`). Use this mode before making claims such as "BDLS is faster
  than Raft" because it exposes one-block burst artifacts and run-to-run
  variance.
* `trace`: run the sustained BDLS 7/13/25-orderer shape with
  `orderer.consensus.bdls=debug` and emit BDLS finality/signature/ledger trace
  TSVs for bottleneck attribution.
* `paper`: reproduce the 1500-transaction block shape used in the Fabric BDLS
  paper for 4, 5, and 6 orderer BFT clusters.
* `scale`: compare 4, 5, 6, and 7 orderer BFT clusters using filled
  1500-transaction blocks, plus peer-count variants.

The planned hypotheses are:

* **H1: Filled-block throughput.** BDLS throughput should be evaluated with
  `BENCH_N == MaxMessageCount`; otherwise batch timeout dominates the result.
* **H2: Orderer scaling.** BDLS should degrade more slowly than SmartBFT as
  orderer count grows from 7 to 25 because its fast path is star-shaped.
* **H3: Timeout sensitivity.** BDLS `LatencyMs` and delta knobs should affect
  recovery behavior more than saturated honest-leader throughput.
* **H4: Fault recovery.** Under one stalled or killed orderer, BDLS should
  preserve safety and recover liveness through round-change/decide propagation.
* **H5: Fabric signature quorum overhead.** The current integration must gather
  BFT-format Fabric block signatures after BDLS decides. If the larger-cluster
  slowdown tracks that post-decision quorum exchange rather than the BDLS
  decision itself, the next optimization should piggyback or pipeline Fabric
  metadata signatures instead of only tuning BDLS round timers.
* **H6: Integration-vs-consensus split.** The claim that BDLS is better than
  SmartBFT should be accepted for ordering-service throughput only when the
  sustained sweep remains ahead, and accepted for pure consensus only when the
  finality-duration distribution is ahead under the same block and network
  conditions. These are separate claims and should not be collapsed.

Before treating these numbers as deployment guidance, run multiple samples for
each point and record mean, min, max, standard deviation, and coefficient of
variation. The sweep script writes `quality_note=single_or_low_sample` when a
case has fewer than three samples and `quality_note=high_variance` when the
TPS coefficient of variation is above 20 percent. For a final throughput study,
run the matrix on dedicated hosts or containers with controlled CPU, memory,
disk, and network resources. The local developer-machine numbers above are
regression evidence, not capacity-planning data.
