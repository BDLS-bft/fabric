#!/usr/bin/env python3
#
# Copyright IBM Corp. All Rights Reserved.
#
# SPDX-License-Identifier: Apache-2.0
#
"""Summarize consensus-stage Prometheus histogram snapshots for comparison.

This script consumes Prometheus text-format snapshots collected from orderer
operations endpoints and reports comparable mean/p95 rows for the consensus
stage metrics used by the BDLS performance proof.
"""

from __future__ import annotations

import argparse
import math
import re
import sys
from pathlib import Path


TARGETS = [
    ("BDLS", "consensus_bdls_consensus_finality_duration", "bft_finality"),
    ("BDLS", "consensus_bdls_block_signature_duration", "fabric_bft_signature_quorum"),
    ("BDLS", "consensus_bdls_ledger_write_duration", "ledger_write"),
    ("BDLS", "consensus_bdls_commit_pipeline_duration", "commit_pipeline"),
    ("SmartBFT", "consensus_BFT_proposal_finality_duration", "bft_proposal_finality"),
    ("SmartBFT", "consensus_smartbft_consensus_latency_sync", "bft_consensus_latency"),
    ("SmartBFT", "consensus_smartbft_pool_latency_of_elements", "bft_request_pool_latency"),
    ("SmartBFT", "consensus_smartbft_view_latency_batch_processing", "bft_batch_processing"),
    ("SmartBFT", "consensus_smartbft_view_latency_batch_save", "bft_batch_persist"),
    ("etcdraft", "consensus_etcdraft_data_persist_duration", "raft_persistence"),
]

HEADERS = [
    "label",
    "consensus",
    "orderers",
    "peers_per_org",
    "max_message_count",
    "bench_n",
    "concurrency",
    "sample",
    "run_type",
    "stage",
    "metric",
    "snapshots",
    "count",
    "mean_s",
    "p95_s",
]

SAMPLE_RE = re.compile(
    r"^(?P<name>[a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(?P<labels>[^}]*)\})?\s+(?P<value>[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?|[+-]Inf|NaN)"
)


def parse_labels(raw: str | None) -> dict[str, str]:
    if raw is None or raw == "":
        return {}
    labels: dict[str, str] = {}
    for item in re.finditer(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:\\"|[^"])*)"', raw):
        labels[item.group(1)] = item.group(2).replace('\\"', '"')
    return labels


def parse_value(raw: str) -> float:
    if raw == "+Inf":
        return math.inf
    if raw == "-Inf":
        return -math.inf
    if raw == "NaN":
        return math.nan
    return float(raw)


class Histogram:
    def __init__(self) -> None:
        self.buckets: dict[float, float] = {}
        self.total = 0.0
        self.count = 0.0

    def add_bucket(self, le: float, value: float) -> None:
        self.buckets[le] = self.buckets.get(le, 0.0) + value

    def add_sum(self, value: float) -> None:
        self.total += value

    def add_count(self, value: float) -> None:
        self.count += value

    def mean(self) -> str:
        if self.count <= 0:
            return ""
        return format_float(self.total / self.count)

    def p95(self) -> str:
        if self.count <= 0 or not self.buckets:
            return ""
        target = self.count * 0.95
        for le in sorted(self.buckets):
            if self.buckets[le] >= target:
                if math.isinf(le):
                    return ""
                return format_float(le)
        return ""


def format_float(value: float) -> str:
    if math.isnan(value) or math.isinf(value):
        return ""
    return f"{value:.9f}".rstrip("0").rstrip(".")


def parse_snapshot(path: Path) -> dict[str, Histogram]:
    histograms: dict[str, Histogram] = {}
    with path.open("r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            match = SAMPLE_RE.match(line.strip())
            if match is None:
                continue
            name = match.group("name")
            labels = parse_labels(match.group("labels"))
            value = parse_value(match.group("value"))

            if name.endswith("_bucket"):
                metric = name[: -len("_bucket")]
                le = parse_value(labels.get("le", ""))
                histograms.setdefault(metric, Histogram()).add_bucket(le, value)
            elif name.endswith("_sum"):
                metric = name[: -len("_sum")]
                histograms.setdefault(metric, Histogram()).add_sum(value)
            elif name.endswith("_count"):
                metric = name[: -len("_count")]
                histograms.setdefault(metric, Histogram()).add_count(value)
    return histograms


def source_metadata(path: Path) -> dict[str, str]:
    name = path.name
    patterns = {
        "label": r"^(.*?)\.consensus-",
        "consensus": r"\.consensus-([^.]+)",
        "orderers": r"\.orderers-([^.]+)",
        "peers_per_org": r"\.peers-([^.]+)",
        "max_message_count": r"\.max-([^.]+)",
        "bench_n": r"\.n-([^.]+)",
        "concurrency": r"\.concurrency-([^.]+)",
        "sample": r"\.sample-([^.]+)",
        "run_type": r"\.run-([^.]+)",
    }
    metadata = {key: "" for key in patterns}
    for key, pattern in patterns.items():
        match = re.search(pattern, name)
        if match is not None:
            metadata[key] = match.group(1)
    return metadata


def merge_histograms(target: Histogram, source: Histogram) -> None:
    for le, value in source.buckets.items():
        target.add_bucket(le, value)
    target.add_sum(source.total)
    target.add_count(source.count)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("snapshots", nargs="+", type=Path, help="Prometheus text-format metric snapshots")
    args = parser.parse_args()

    grouped: dict[tuple[str, ...], Histogram] = {}
    snapshot_counts: dict[tuple[str, ...], int] = {}
    for path in args.snapshots:
        metadata = source_metadata(path)
        histograms = parse_snapshot(path)
        for consensus, metric, stage in TARGETS:
            histogram = histograms.get(metric)
            if histogram is None or histogram.count <= 0:
                continue
            key = (
                metadata["label"],
                metadata["consensus"] or consensus,
                metadata["orderers"],
                metadata["peers_per_org"],
                metadata["max_message_count"],
                metadata["bench_n"],
                metadata["concurrency"],
                metadata["sample"],
                metadata["run_type"],
                stage,
                metric,
            )
            merge_histograms(grouped.setdefault(key, Histogram()), histogram)
            snapshot_counts[key] = snapshot_counts.get(key, 0) + 1

    print("\t".join(HEADERS))
    for key in sorted(grouped):
        histogram = grouped[key]
        print(
            "\t".join(
                [
                    *key,
                    str(snapshot_counts[key]),
                    format_float(histogram.count),
                    histogram.mean(),
                    histogram.p95(),
                ]
            )
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
