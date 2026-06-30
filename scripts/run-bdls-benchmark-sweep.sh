#!/bin/bash
#
# Copyright IBM Corp. All Rights Reserved.
#
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

fabric_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$fabric_dir"

mode="${1:-smoke}"
bench_mode="${BENCH_MODE:-broadcast}"
bench_n="${BENCH_N:-200}"
bench_repeats="${BENCH_REPEATS:-1}"
concurrency="${BENCH_CONCURRENCY:-32}"
payload_bytes="${BENCH_PAYLOAD_BYTES:-1}"
batch_timeout="${BENCH_BATCH_TIMEOUT:-1s}"
commit_timeout="${BENCH_COMMIT_TIMEOUT:-5m}"
preferred_max_bytes_kb="${BENCH_PREFERRED_MAX_BYTES_KB:-512}"
absolute_max_bytes_mb="${BENCH_ABSOLUTE_MAX_BYTES_MB:-10}"
bdls_latency="${BENCH_BDLS_LATENCY:-100ms}"
bdls_delta0="${BENCH_BDLS_DELTA0:-0}"
bdls_delta1="${BENCH_BDLS_DELTA1:-0}"
bdls_delta_prime1="${BENCH_BDLS_DELTA_PRIME1:-0}"
bdls_delta2="${BENCH_BDLS_DELTA2:-0}"
bdls_delta3="${BENCH_BDLS_DELTA3:-0}"
bdls_reliable_decide="${BENCH_BDLS_RELIABLE_DECIDE:-true}"
bdls_compact_state="${BENCH_BDLS_COMPACT_STATE:-true}"
bench_warmup_repeats="${BENCH_WARMUP_REPEATS:-0}"
bench_log_spec="${BENCH_LOG_SPEC:-orderer.consensus.bdls=info:orderer.consensus.smartbft=info:orderer.consensus.etcdraft=info}"
bench_trace_decisions="${BENCH_TRACE_DECISIONS:-false}"

out_dir="${BENCH_OUT_DIR:-_benchmarks/bdls}"
mkdir -p "$out_dir"
out_file="$out_dir/${mode}-$(date +%Y%m%d-%H%M%S).txt"
summary_file="${out_file%.txt}.summary.tsv"
aggregate_file="${out_file%.txt}.aggregate.tsv"
trace_file="${out_file%.txt}.bdls_trace.tsv"
trace_aggregate_file="${out_file%.txt}.bdls_trace_aggregate.tsv"
metrics_dir="$fabric_dir/${out_file%.txt}.metrics"
metrics_summary_file="${out_file%.txt}.metrics.tsv"
git_sha="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

run_case() {
  local consensus="$1"
  local orderers="$2"
  local peers_per_org="$3"
  local max_message_count="$4"
  local label="$5"

  run_sample() {
    local sample="$1"
    local run_type="$2"
    local metrics_prefix="${label}.consensus-${consensus}.orderers-${orderers}.peers-${peers_per_org}.max-${max_message_count}.n-${bench_n}.concurrency-${concurrency}.sample-${sample}.run-${run_type}"

    printf "\n=== %s consensus=%s orderers=%s peers_per_org=%s max_message_count=%s bench_mode=%s concurrency=%s bench_n=%s sample=%s run_type=%s bdls_reliable_decide=%s bdls_compact_state=%s run_sha=%s ===\n" \
      "$label" "$consensus" "$orderers" "$peers_per_org" "$max_message_count" "$bench_mode" "$concurrency" "$bench_n" "$sample" "$run_type" "$bdls_reliable_decide" "$bdls_compact_state" "$git_sha" | tee -a "$out_file"

    {
      go test ./integration/bdls \
        -run '^$' \
        -bench "^BenchmarkOrderingThroughput/${consensus}$" \
        -benchtime "${bench_n}x" \
        -count 1 \
        -timeout 2h \
        -args \
        -bdls.bench.mode "$bench_mode" \
        -bdls.bench.consensus "$consensus" \
        -bdls.bench.orderers "$orderers" \
        -bdls.bench.peers-per-org "$peers_per_org" \
        -bdls.bench.max-message-count "$max_message_count" \
        -bdls.bench.batch-timeout "$batch_timeout" \
        -bdls.bench.commit-timeout "$commit_timeout" \
        -bdls.bench.absolute-max-bytes-mb "$absolute_max_bytes_mb" \
        -bdls.bench.preferred-max-bytes-kb "$preferred_max_bytes_kb" \
        -bdls.bench.payload-bytes "$payload_bytes" \
        -bdls.bench.latency "$bdls_latency" \
        -bdls.bench.delta0 "$bdls_delta0" \
        -bdls.bench.delta1 "$bdls_delta1" \
        -bdls.bench.delta-prime1 "$bdls_delta_prime1" \
        -bdls.bench.delta2 "$bdls_delta2" \
        -bdls.bench.delta3 "$bdls_delta3" \
        -bdls.bench.reliable-decide="$bdls_reliable_decide" \
        -bdls.bench.compact-state="$bdls_compact_state" \
        -bdls.bench.log-spec "$bench_log_spec" \
        -bdls.bench.trace-decisions="$bench_trace_decisions" \
        -bdls.bench.metrics-dir "$metrics_dir" \
        -bdls.bench.metrics-prefix "$metrics_prefix" \
        -bdls.bench.concurrency "$concurrency"
    } 2>&1 | tee -a "$out_file"
  }

  local sample
  if [ "$bench_warmup_repeats" -gt 0 ]; then
    for sample in $(seq 1 "$bench_warmup_repeats"); do
      run_sample "$sample" "warmup"
    done
  fi

  if [ "$bench_repeats" -gt 0 ]; then
    for sample in $(seq 1 "$bench_repeats"); do
      run_sample "$sample" "measured"
    done
  fi
}

run_filled_case() {
  local consensus="$1"
  local orderers="$2"
  local peers_per_org="$3"
  local max_message_count="$4"
  local label="$5"
  bench_n="${BENCH_N:-$max_message_count}"
  run_case "$consensus" "$orderers" "$peers_per_org" "$max_message_count" "$label"
}

case "$mode" in
  smoke)
    bench_n="${BENCH_N:-100}"
    concurrency="${BENCH_CONCURRENCY:-16}"
    run_case BDLS 4 1 100 smoke
    run_case BFT 4 1 100 smoke
    run_case etcdraft 3 1 100 smoke
    ;;
  tuning)
    bench_n="${BENCH_N:-100}"
    concurrency="${BENCH_CONCURRENCY:-16}"
    run_case BDLS 4 1 100 tuning-block100
    run_case BFT 4 1 100 tuning-block100
    run_case etcdraft 3 1 100 tuning-block100
    bench_n="${BENCH_N:-1000}"
    concurrency="${BENCH_CONCURRENCY:-32}"
    run_case BDLS 4 1 1000 tuning-block1000
    (
      bdls_latency="${BENCH_BDLS_LATENCY_FAST:-20ms}"
      bdls_delta0="${BENCH_BDLS_DELTA0_FAST:-10ms}"
      bdls_delta1="${BENCH_BDLS_DELTA1_FAST:-20ms}"
      bdls_delta_prime1="${BENCH_BDLS_DELTA_PRIME1_FAST:-10ms}"
      bdls_delta2="${BENCH_BDLS_DELTA2_FAST:-10ms}"
      bdls_delta3="${BENCH_BDLS_DELTA3_FAST:-10ms}"
      run_case BDLS 4 1 1000 tuning-bdls-fast-local
    )
    run_case BFT 4 1 1000 tuning-block1000
    run_case etcdraft 3 1 1000 tuning-block1000
    ;;
  paper)
    concurrency="${BENCH_CONCURRENCY:-64}"
    run_filled_case BDLS 4 1 1500 paper
    run_filled_case BDLS 5 1 1500 paper
    run_filled_case BDLS 6 1 1500 paper
    run_filled_case BFT 4 1 1500 paper
    run_filled_case BFT 5 1 1500 paper
    run_filled_case BFT 6 1 1500 paper
    run_filled_case etcdraft 3 1 1500 paper
    run_filled_case etcdraft 5 1 1500 paper
    ;;
  focused)
    bench_n="${BENCH_N:-1000}"
    concurrency="${BENCH_CONCURRENCY:-32}"
    preferred_max_bytes_kb="${BENCH_PREFERRED_MAX_BYTES_KB:-2048}"
    run_case BDLS 4 1 1000 focused-local-default
    (
      bdls_latency="${BENCH_BDLS_LATENCY_FAST:-20ms}"
      bdls_delta0="${BENCH_BDLS_DELTA0_FAST:-10ms}"
      bdls_delta1="${BENCH_BDLS_DELTA1_FAST:-20ms}"
      bdls_delta_prime1="${BENCH_BDLS_DELTA_PRIME1_FAST:-10ms}"
      bdls_delta2="${BENCH_BDLS_DELTA2_FAST:-10ms}"
      bdls_delta3="${BENCH_BDLS_DELTA3_FAST:-10ms}"
      run_case BDLS 4 1 1000 focused-local-fast
      run_case BDLS 7 1 1000 focused-local-fast
    )
    run_case BFT 4 1 1000 focused-local
    run_case BFT 7 1 1000 focused-local
    run_case etcdraft 3 1 1000 focused-local
    run_case etcdraft 7 1 1000 focused-local
    ;;
  sustained)
    bench_n="${BENCH_N:-50000}"
    bench_repeats="${BENCH_REPEATS:-3}"
    concurrency="${BENCH_CONCURRENCY:-64}"
    preferred_max_bytes_kb="${BENCH_PREFERRED_MAX_BYTES_KB:-2048}"
    (
      bdls_latency="${BENCH_BDLS_LATENCY_SUSTAINED:-100ms}"
      bdls_delta0="${BENCH_BDLS_DELTA0_SUSTAINED:-50ms}"
      bdls_delta1="${BENCH_BDLS_DELTA1_SUSTAINED:-100ms}"
      bdls_delta_prime1="${BENCH_BDLS_DELTA_PRIME1_SUSTAINED:-50ms}"
      bdls_delta2="${BENCH_BDLS_DELTA2_SUSTAINED:-50ms}"
      bdls_delta3="${BENCH_BDLS_DELTA3_SUSTAINED:-50ms}"
      run_case BDLS 7 1 1000 sustained-local-balanced
      run_case BDLS 13 1 1000 sustained-local-balanced
      run_case BDLS 25 1 1000 sustained-local-balanced
    )
    run_case BFT 7 1 1000 sustained-local
    run_case BFT 13 1 1000 sustained-local
    run_case BFT 25 1 1000 sustained-local
    run_case etcdraft 7 1 1000 sustained-local
    run_case etcdraft 13 1 1000 sustained-local
    run_case etcdraft 25 1 1000 sustained-local
    ;;
  trace)
    bench_n="${BENCH_N:-50000}"
    bench_repeats="${BENCH_REPEATS:-1}"
    concurrency="${BENCH_CONCURRENCY:-64}"
    preferred_max_bytes_kb="${BENCH_PREFERRED_MAX_BYTES_KB:-2048}"
    bench_log_spec="${BENCH_LOG_SPEC:-orderer.consensus.bdls=debug:orderer.consensus.bdls.trace=debug:orderer.common.server=info}"
    bench_trace_decisions="${BENCH_TRACE_DECISIONS:-true}"
    (
      bdls_latency="${BENCH_BDLS_LATENCY_TRACE:-100ms}"
      bdls_delta0="${BENCH_BDLS_DELTA0_TRACE:-50ms}"
      bdls_delta1="${BENCH_BDLS_DELTA1_TRACE:-100ms}"
      bdls_delta_prime1="${BENCH_BDLS_DELTA_PRIME1_TRACE:-50ms}"
      bdls_delta2="${BENCH_BDLS_DELTA2_TRACE:-50ms}"
      bdls_delta3="${BENCH_BDLS_DELTA3_TRACE:-50ms}"
      run_case BDLS 7 1 1000 trace-local-balanced
      run_case BDLS 13 1 1000 trace-local-balanced
      run_case BDLS 25 1 1000 trace-local-balanced
    )
    ;;
  diagnostic)
    bench_n="${BENCH_N:-1000}"
    concurrency="${BENCH_CONCURRENCY:-32}"
    preferred_max_bytes_kb="${BENCH_PREFERRED_MAX_BYTES_KB:-2048}"
    run_case BDLS 4 1 500 diagnostic-default-block500
    run_case BDLS 7 1 500 diagnostic-default-block500
    run_case BDLS 4 1 1000 diagnostic-default-block1000
    run_case BDLS 7 1 1000 diagnostic-default-block1000
    (
      bdls_latency="${BENCH_BDLS_LATENCY_FAST:-20ms}"
      bdls_delta0="${BENCH_BDLS_DELTA0_FAST:-10ms}"
      bdls_delta1="${BENCH_BDLS_DELTA1_FAST:-20ms}"
      bdls_delta_prime1="${BENCH_BDLS_DELTA_PRIME1_FAST:-10ms}"
      bdls_delta2="${BENCH_BDLS_DELTA2_FAST:-10ms}"
      bdls_delta3="${BENCH_BDLS_DELTA3_FAST:-10ms}"
      run_case BDLS 4 1 500 diagnostic-fast-block500
      run_case BDLS 7 1 500 diagnostic-fast-block500
      run_case BDLS 4 1 1000 diagnostic-fast-block1000
      run_case BDLS 7 1 1000 diagnostic-fast-block1000
    )
    ;;
  scale)
    concurrency="${BENCH_CONCURRENCY:-64}"
    run_filled_case BDLS 4 1 1500 scale-orderers
    run_filled_case BDLS 5 1 1500 scale-orderers
    run_filled_case BDLS 6 1 1500 scale-orderers
    run_filled_case BDLS 7 1 1500 scale-orderers
    run_filled_case BFT 4 1 1500 scale-orderers
    run_filled_case BFT 5 1 1500 scale-orderers
    run_filled_case BFT 6 1 1500 scale-orderers
    run_filled_case BFT 7 1 1500 scale-orderers
    run_filled_case etcdraft 3 1 1500 scale-orderers
    run_filled_case etcdraft 5 1 1500 scale-orderers
    run_filled_case etcdraft 7 1 1500 scale-orderers
    run_filled_case BDLS 4 2 1500 scale-peers
    run_filled_case BFT 4 2 1500 scale-peers
    run_filled_case etcdraft 3 2 1500 scale-peers
    ;;
  *)
    echo "usage: $0 [smoke|tuning|focused|sustained|trace|diagnostic|paper|scale]" >&2
    exit 2
    ;;
esac

echo "benchmark output: $out_file"

awk '
  BEGIN {
    OFS = "\t"
    print "label", "consensus", "orderers", "peers_per_org", "max_message_count", "bench_n", "concurrency", "sample", "tx_per_s", "send_tx_per_s", "send_s", "commit_wait_s", "blocks", "tx_per_block", "block_per_s"
  }
  /^=== / {
    label = $2
    run_type = "measured"
    consensus = orderers = peers = max_messages = concurrency = bench_n = sample = ""
    for (i = 3; i <= NF; i++) {
      split($i, kv, "=")
      if (kv[1] == "consensus") consensus = kv[2]
      if (kv[1] == "orderers") orderers = kv[2]
      if (kv[1] == "peers_per_org") peers = kv[2]
      if (kv[1] == "max_message_count") max_messages = kv[2]
      if (kv[1] == "concurrency") concurrency = kv[2]
      if (kv[1] == "bench_n") bench_n = kv[2]
      if (kv[1] == "sample") sample = kv[2]
      if (kv[1] == "run_type") run_type = kv[2]
    }
  }
  $1 ~ /^[0-9]+$/ && /tx\/s/ && run_type == "measured" {
    tx = send_tx = send_s = commit_wait = blocks = tx_block = block_s = ""
    for (i = 4; i < NF; i += 2) {
      if ($(i + 1) == "tx/s") tx = $i
      if ($(i + 1) == "send_tx/s") send_tx = $i
      if ($(i + 1) == "send_s") send_s = $i
      if ($(i + 1) == "commit_wait_s") commit_wait = $i
      if ($(i + 1) == "blocks") blocks = $i
      if ($(i + 1) == "tx/block") tx_block = $i
      if ($(i + 1) == "block/s") block_s = $i
    }
    print label, consensus, orderers, peers, max_messages, bench_n, concurrency, sample, tx, send_tx, send_s, commit_wait, blocks, tx_block, block_s
  }
' "$out_file" > "$summary_file"
echo "benchmark summary: $summary_file"

awk '
  BEGIN {
    FS = OFS = "\t"
    print "label", "consensus", "orderers", "peers_per_org", "max_message_count", "bench_n", "concurrency", "samples", "tx_per_s_mean", "tx_per_s_min", "tx_per_s_max", "tx_per_s_stdev", "tx_per_s_cv_pct", "send_tx_per_s_mean", "send_s_mean", "commit_wait_s_mean", "block_per_s_mean", "quality_note"
  }
  NR == 1 {
    next
  }
  {
    key = $1 SUBSEP $2 SUBSEP $3 SUBSEP $4 SUBSEP $5 SUBSEP $6 SUBSEP $7
    if (!(key in seen)) {
      seen[key] = 1
      order[++order_count] = key
      label[key] = $1
      consensus[key] = $2
      orderers[key] = $3
      peers[key] = $4
      max_messages[key] = $5
      bench_n[key] = $6
      concurrency[key] = $7
      tx_min[key] = $9 + 0
      tx_max[key] = $9 + 0
    }
    count[key]++
    tx = $9 + 0
    send_tx = $10 + 0
    send_s = $11 + 0
    commit_wait = $12 + 0
    block_s = $15 + 0
    tx_sum[key] += tx
    tx_sum_sq[key] += tx * tx
    send_tx_sum[key] += send_tx
    send_s_sum[key] += send_s
    commit_wait_sum[key] += commit_wait
    block_s_sum[key] += block_s
    if (tx < tx_min[key]) tx_min[key] = tx
    if (tx > tx_max[key]) tx_max[key] = tx
  }
  END {
    for (i = 1; i <= order_count; i++) {
      key = order[i]
      samples = count[key]
      tx_mean = tx_sum[key] / samples
      variance = (tx_sum_sq[key] / samples) - (tx_mean * tx_mean)
      if (variance < 0) variance = 0
      tx_stdev = sqrt(variance)
      tx_cv_pct = 0
      if (tx_mean > 0) tx_cv_pct = (tx_stdev / tx_mean) * 100
      quality_note = "ok"
      if (samples < 3) {
        quality_note = "single_or_low_sample"
      } else if (tx_cv_pct > 20) {
        quality_note = "high_variance"
      }
      print label[key], consensus[key], orderers[key], peers[key], max_messages[key], bench_n[key], concurrency[key], samples, tx_mean, tx_min[key], tx_max[key], tx_stdev, tx_cv_pct, send_tx_sum[key] / samples, send_s_sum[key] / samples, commit_wait_sum[key] / samples, block_s_sum[key] / samples, quality_note
    }
  }
' "$summary_file" > "$aggregate_file"
echo "benchmark aggregate: $aggregate_file"

metrics_snapshots="$(find "$metrics_dir" -type f -name '*.prom' 2>/dev/null | sort || true)"
if [ -n "$metrics_snapshots" ]; then
  # shellcheck disable=SC2086
  scripts/compare-consensus-metrics.py $metrics_snapshots > "$metrics_summary_file"
  echo "benchmark metrics summary: $metrics_summary_file"
fi

if [ "$mode" = "trace" ]; then
  scripts/parse-bdls-trace.py "$out_file" > "$trace_file"
  scripts/parse-bdls-trace.py --aggregate "$out_file" > "$trace_aggregate_file"
  echo "BDLS trace summary: $trace_file"
  echo "BDLS trace aggregate: $trace_aggregate_file"
fi
