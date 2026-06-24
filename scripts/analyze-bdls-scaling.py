#!/usr/bin/env python3
#
# Copyright IBM Corp. All Rights Reserved.
#
# SPDX-License-Identifier: Apache-2.0
#
"""Join BDLS throughput and trace aggregates to classify scaling bottlenecks."""

from __future__ import annotations

import argparse
import csv
import sys
from pathlib import Path


HEADERS = [
    "orderers",
    "tx_per_s_mean",
    "send_tx_per_s_mean",
    "commit_wait_s_mean",
    "consensus_finality_mean_s",
    "block_signature_mean_s",
    "ledger_write_mean_s",
    "commit_pipeline_mean_s",
    "signature_to_finality_ratio",
    "tx_drop_vs_previous_pct",
    "finality_growth_vs_previous",
    "signature_growth_vs_previous",
    "likely_bottleneck",
]


def read_tsv(path: Path) -> list[dict[str, str]]:
    with path.open("r", encoding="utf-8", newline="") as handle:
        return list(csv.DictReader(handle, delimiter="\t"))


def number(row: dict[str, str], key: str) -> float | None:
    value = row.get(key, "")
    if value == "":
        return None
    return float(value)


def rows_by_orderers(rows: list[dict[str, str]]) -> dict[int, list[dict[str, str]]]:
    grouped: dict[int, list[dict[str, str]]] = {}
    for row in rows:
        if row.get("run_type", "measured") == "warmup":
            continue
        consensus = row.get("consensus", "")
        if consensus not in ("", "BDLS"):
            continue
        orderers = row.get("orderers", "")
        if orderers == "":
            continue
        grouped.setdefault(int(orderers), []).append(row)
    return grouped


def weighted_mean(rows: list[dict[str, str]], value_key: str, weight_key: str) -> float | None:
    total = 0.0
    weight_total = 0.0
    for row in rows:
        value = number(row, value_key)
        weight = number(row, weight_key)
        if value is None:
            continue
        if weight is None or weight <= 0:
            weight = 1.0
        total += value * weight
        weight_total += weight
    if weight_total == 0:
        return None
    return total / weight_total


def ratio(current: float | None, previous: float | None) -> float | None:
    if current is None or previous is None or previous == 0:
        return None
    return current / previous


def format_number(value: float | None) -> str:
    if value is None:
        return ""
    return f"{value:.6f}".rstrip("0").rstrip(".")


def classify(
    tx_drop_pct: float | None,
    finality_growth: float | None,
    signature_growth: float | None,
    send_drop_pct: float | None,
) -> str:
    if tx_drop_pct is None or tx_drop_pct < 10:
        return "no_material_drop"
    if signature_growth is not None and finality_growth is not None and signature_growth > finality_growth * 1.25:
        return "post_decision_signature_quorum"
    if finality_growth is not None and finality_growth > 1.25:
        return "bdls_consensus_finality"
    if send_drop_pct is not None and send_drop_pct >= tx_drop_pct * 0.7:
        return "client_ingress_or_local_backpressure"
    return "mixed_or_unattributed"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--throughput", required=True, type=Path, help="*.aggregate.tsv from run-bdls-benchmark-sweep.sh")
    parser.add_argument("--trace", required=True, type=Path, help="*.bdls_trace_aggregate.tsv from trace mode")
    args = parser.parse_args()

    throughput = rows_by_orderers(read_tsv(args.throughput))
    trace = rows_by_orderers(read_tsv(args.trace))
    orderer_counts = sorted(set(throughput) & set(trace))
    if not orderer_counts:
        print("no matching BDLS orderer counts found", file=sys.stderr)
        return 1

    print("\t".join(HEADERS))
    previous: tuple[list[dict[str, str]], list[dict[str, str]]] | None = None
    for orderers in orderer_counts:
        throughput_rows = throughput[orderers]
        trace_rows = trace[orderers]
        tx = weighted_mean(throughput_rows, "tx_per_s_mean", "samples")
        send_tx = weighted_mean(throughput_rows, "send_tx_per_s_mean", "samples")
        commit_wait = weighted_mean(throughput_rows, "commit_wait_s_mean", "samples")
        finality = weighted_mean(trace_rows, "consensus_finality_mean_s", "blocks")
        signature = weighted_mean(trace_rows, "block_signature_mean_s", "blocks")
        ledger = weighted_mean(trace_rows, "ledger_write_mean_s", "blocks")
        pipeline = weighted_mean(trace_rows, "commit_pipeline_mean_s", "blocks")
        signature_to_finality = ratio(signature, finality)

        tx_drop_pct = finality_growth = signature_growth = send_drop_pct = None
        if previous is not None:
            previous_throughput_rows, previous_trace_rows = previous
            previous_tx = weighted_mean(previous_throughput_rows, "tx_per_s_mean", "samples")
            previous_send_tx = weighted_mean(previous_throughput_rows, "send_tx_per_s_mean", "samples")
            previous_finality = weighted_mean(previous_trace_rows, "consensus_finality_mean_s", "blocks")
            previous_signature = weighted_mean(previous_trace_rows, "block_signature_mean_s", "blocks")
            if tx is not None and previous_tx is not None and previous_tx > 0:
                tx_drop_pct = max(0.0, (1 - (tx / previous_tx)) * 100)
            if send_tx is not None and previous_send_tx is not None and previous_send_tx > 0:
                send_drop_pct = max(0.0, (1 - (send_tx / previous_send_tx)) * 100)
            finality_growth = ratio(finality, previous_finality)
            signature_growth = ratio(signature, previous_signature)

        print(
            "\t".join(
                [
                    str(orderers),
                    format_number(tx),
                    format_number(send_tx),
                    format_number(commit_wait),
                    format_number(finality),
                    format_number(signature),
                    format_number(ledger),
                    format_number(pipeline),
                    format_number(signature_to_finality),
                    format_number(tx_drop_pct),
                    format_number(finality_growth),
                    format_number(signature_growth),
                    classify(tx_drop_pct, finality_growth, signature_growth, send_drop_pct),
                ]
            )
        )
        previous = (throughput_rows, trace_rows)

    return 0


if __name__ == "__main__":
    sys.exit(main())
