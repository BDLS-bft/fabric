#!/usr/bin/env python3
#
# Copyright IBM Corp. All Rights Reserved.
#
# SPDX-License-Identifier: Apache-2.0
#
"""Extract BDLS decision-stage trace timing from orderer logs.

The BDLS adapter emits debug traces for each decided block when
FABRIC_LOGGING_SPEC includes orderer.consensus.bdls=debug. This parser turns
those free-form log lines into TSV so scale runs can distinguish BDLS finality
from post-decision Fabric signature quorum and ledger-write costs.
"""

from __future__ import annotations

import argparse
import re
import statistics
import sys
from pathlib import Path
from typing import Iterable


DURATION_UNITS = {
    "ns": 1e-9,
    "us": 1e-6,
    "µs": 1e-6,
    "ms": 1e-3,
    "s": 1.0,
    "m": 60.0,
    "h": 3600.0,
}

HEADERS = [
    "source",
    "line",
    "label",
    "consensus",
    "orderers",
    "peers_per_org",
    "max_message_count",
    "bench_n",
    "concurrency",
    "sample",
    "run_type",
    "block",
    "height",
    "round",
    "envelopes",
    "consensus_finality_s",
    "commit_pipeline_s",
    "block_signature_s",
    "ledger_write_s",
    "inbound_messages",
    "inbound_bytes",
    "inbound_proofs",
    "inbound_proof_bytes",
    "outbound_messages",
    "outbound_bytes",
    "outbound_proofs",
    "outbound_proof_bytes",
]

AGGREGATE_HEADERS = [
    "source",
    "label",
    "consensus",
    "orderers",
    "peers_per_org",
    "max_message_count",
    "bench_n",
    "concurrency",
    "sample",
    "run_type",
    "blocks",
    "consensus_finality_mean_s",
    "consensus_finality_p50_s",
    "consensus_finality_p95_s",
    "consensus_finality_max_s",
    "block_signature_mean_s",
    "block_signature_p50_s",
    "block_signature_p95_s",
    "block_signature_max_s",
    "ledger_write_mean_s",
    "commit_pipeline_mean_s",
    "signature_to_finality_ratio",
    "inbound_messages_mean",
    "outbound_messages_mean",
    "inbound_bytes_mean",
    "outbound_bytes_mean",
    "inbound_proof_bytes_mean",
    "outbound_proof_bytes_mean",
]


def duration_seconds(value: str) -> str:
    match = re.fullmatch(r"([0-9]+(?:\.[0-9]+)?)(ns|us|µs|ms|s|m|h)", value)
    if match is None:
        return ""
    amount, unit = match.groups()
    return f"{float(amount) * DURATION_UNITS[unit]:.9f}".rstrip("0").rstrip(".")


def first_group(patterns: Iterable[str], text: str) -> str:
    for pattern in patterns:
        match = re.search(pattern, text, re.IGNORECASE)
        if match is not None:
            return match.group(1)
    return ""


def first_duration(names: Iterable[str], text: str) -> str:
    labels = "|".join(re.escape(name) for name in names)
    value = first_group(
        [
            rf"(?:{labels})\s*[=:]\s*([0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h))",
            rf"(?:{labels})\s+([0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h))",
        ],
        text,
    )
    if value == "":
        return ""
    return duration_seconds(value)


def section(name: str, text: str) -> str:
    match = re.search(rf"{name}\b(.*?)(?:\boutbound\b|\binbound\b|$)", text, re.IGNORECASE)
    if match is None:
        return ""
    return match.group(1)


def bucket_values(text: str, bucket: str) -> tuple[str, str, str, str]:
    area = section(bucket, text)
    messages = first_group(
        [
            r"(?:message_count|messages|count)\s*[=:]\s*([0-9]+)",
        ],
        area,
    )
    byte_count = first_group(
        [
            r"(?:message_bytes|signed_bytes|bytes)\s*[=:]\s*([0-9]+)",
        ],
        area,
    )
    proofs = first_group(
        [
            r"(?:proof_count|proofs)\s*[=:]\s*([0-9]+)",
        ],
        area,
    )
    proof_bytes = first_group(
        [
            r"proof_bytes\s*[=:]\s*([0-9]+)",
        ],
        area,
    )
    return messages, byte_count, proofs, proof_bytes


def parse_case_marker(line: str) -> dict[str, str] | None:
    if not line.startswith("=== "):
        return None
    parts = line.strip().split()
    if len(parts) < 2:
        return None
    metadata = {
        "label": parts[1],
        "consensus": "",
        "orderers": "",
        "peers_per_org": "",
        "max_message_count": "",
        "bench_n": "",
        "concurrency": "",
        "sample": "",
        "run_type": "",
    }
    for item in parts[2:]:
        if item == "===":
            continue
        key, separator, value = item.partition("=")
        if separator != "":
            metadata[key] = value
    return metadata


def parse_line(source: str, line_number: int, line: str, metadata: dict[str, str]) -> list[str] | None:
    lower = line.lower()
    if "consensus_finality" not in lower or "commit_pipeline" not in lower:
        return None

    inbound_messages, inbound_bytes, inbound_proofs, inbound_proof_bytes = bucket_values(line, "inbound")
    outbound_messages, outbound_bytes, outbound_proofs, outbound_proof_bytes = bucket_values(line, "outbound")

    return [
        source,
        str(line_number),
        metadata.get("label", ""),
        metadata.get("consensus", ""),
        metadata.get("orderers", ""),
        metadata.get("peers_per_org", ""),
        metadata.get("max_message_count", ""),
        metadata.get("bench_n", ""),
        metadata.get("concurrency", ""),
        metadata.get("sample", ""),
        metadata.get("run_type", ""),
        first_group([r"\bblock(?:_number)?\s*[=:]\s*([0-9]+)", r"\bblock\s+([0-9]+)"], line),
        first_group([r"\bheight\s*[=:]\s*([0-9]+)", r"\bheight\s+([0-9]+)"], line),
        first_group([r"\bround\s*[=:]\s*([0-9]+)", r"\bround\s+([0-9]+)"], line),
        first_group([r"\benvelopes\s*[=:]\s*([0-9]+)", r"\benvelope_count\s*[=:]\s*([0-9]+)"], line),
        first_duration(["consensus_finality", "consensus finality"], line),
        first_duration(["commit_pipeline", "commit pipeline"], line),
        first_duration(["block_signature", "block signature"], line),
        first_duration(["ledger_write", "ledger write"], line),
        inbound_messages,
        inbound_bytes,
        inbound_proofs,
        inbound_proof_bytes,
        outbound_messages,
        outbound_bytes,
        outbound_proofs,
        outbound_proof_bytes,
    ]


def parse_file(path: Path) -> Iterable[list[str]]:
    metadata = {
        "label": "",
        "consensus": "",
        "orderers": "",
        "peers_per_org": "",
        "max_message_count": "",
        "bench_n": "",
        "concurrency": "",
        "sample": "",
        "run_type": "",
    }
    with path.open("r", encoding="utf-8", errors="replace") as handle:
        for line_number, line in enumerate(handle, start=1):
            case_metadata = parse_case_marker(line)
            if case_metadata is not None:
                metadata = case_metadata
                continue
            row = parse_line(str(path), line_number, line, metadata)
            if row is not None:
                yield row


def number(value: str) -> float | None:
    if value == "":
        return None
    return float(value)


def values(rows: list[list[str]], header: str) -> list[float]:
    index = HEADERS.index(header)
    return [parsed for row in rows if (parsed := number(row[index])) is not None]


def mean(items: list[float]) -> str:
    if not items:
        return ""
    return f"{statistics.fmean(items):.9f}".rstrip("0").rstrip(".")


def percentile(items: list[float], percent: float) -> str:
    if not items:
        return ""
    ordered = sorted(items)
    offset = (len(ordered) - 1) * percent
    lower = int(offset)
    upper = min(lower + 1, len(ordered) - 1)
    if lower == upper:
        value = ordered[lower]
    else:
        value = ordered[lower] + (ordered[upper] - ordered[lower]) * (offset - lower)
    return f"{value:.9f}".rstrip("0").rstrip(".")


def maximum(items: list[float]) -> str:
    if not items:
        return ""
    return f"{max(items):.9f}".rstrip("0").rstrip(".")


def ratio(numerator: list[float], denominator: list[float]) -> str:
    if not numerator or not denominator:
        return ""
    denominator_mean = statistics.fmean(denominator)
    if denominator_mean == 0:
        return ""
    return f"{statistics.fmean(numerator) / denominator_mean:.3f}".rstrip("0").rstrip(".")


def aggregate(path: Path, rows: list[list[str]]) -> list[str]:
    finality = values(rows, "consensus_finality_s")
    signature = values(rows, "block_signature_s")
    ledger = values(rows, "ledger_write_s")
    pipeline = values(rows, "commit_pipeline_s")
    inbound_messages = values(rows, "inbound_messages")
    outbound_messages = values(rows, "outbound_messages")
    inbound_bytes = values(rows, "inbound_bytes")
    outbound_bytes = values(rows, "outbound_bytes")
    inbound_proof_bytes = values(rows, "inbound_proof_bytes")
    outbound_proof_bytes = values(rows, "outbound_proof_bytes")

    return [
        str(path),
        rows[0][HEADERS.index("label")],
        rows[0][HEADERS.index("consensus")],
        rows[0][HEADERS.index("orderers")],
        rows[0][HEADERS.index("peers_per_org")],
        rows[0][HEADERS.index("max_message_count")],
        rows[0][HEADERS.index("bench_n")],
        rows[0][HEADERS.index("concurrency")],
        rows[0][HEADERS.index("sample")],
        rows[0][HEADERS.index("run_type")],
        str(len(rows)),
        mean(finality),
        percentile(finality, 0.50),
        percentile(finality, 0.95),
        maximum(finality),
        mean(signature),
        percentile(signature, 0.50),
        percentile(signature, 0.95),
        maximum(signature),
        mean(ledger),
        mean(pipeline),
        ratio(signature, finality),
        mean(inbound_messages),
        mean(outbound_messages),
        mean(inbound_bytes),
        mean(outbound_bytes),
        mean(inbound_proof_bytes),
        mean(outbound_proof_bytes),
    ]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--aggregate",
        action="store_true",
        help="emit one summary row per log file instead of one row per decided block",
    )
    parser.add_argument("logs", nargs="+", type=Path, help="BDLS orderer log files")
    args = parser.parse_args()

    if args.aggregate:
        print("\t".join(AGGREGATE_HEADERS))
        for path in args.logs:
            grouped_rows: dict[tuple[str, ...], list[list[str]]] = {}
            for row in parse_file(path):
                key = (
                    row[HEADERS.index("label")],
                    row[HEADERS.index("consensus")],
                    row[HEADERS.index("orderers")],
                    row[HEADERS.index("peers_per_org")],
                    row[HEADERS.index("max_message_count")],
                    row[HEADERS.index("bench_n")],
                    row[HEADERS.index("concurrency")],
                    row[HEADERS.index("sample")],
                    row[HEADERS.index("run_type")],
                )
                grouped_rows.setdefault(key, []).append(row)
            for key in sorted(grouped_rows):
                print("\t".join(aggregate(path, grouped_rows[key])))
    else:
        print("\t".join(HEADERS))
        for path in args.logs:
            for row in parse_file(path):
                print("\t".join(row))
    return 0


if __name__ == "__main__":
    sys.exit(main())
