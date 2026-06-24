#!/bin/bash
#
# Copyright IBM Corp. All Rights Reserved.
#
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: collect-consensus-metrics.sh OUT_DIR URL [URL...]

Fetch Prometheus text-format snapshots from orderer operations metrics
endpoints. URLs should normally point at /metrics, for example:

  scripts/collect-consensus-metrics.sh _benchmarks/metrics \
      http://127.0.0.1:8443/metrics \
      http://127.0.0.1:8444/metrics

Set CURL_ARGS to pass TLS or auth flags through to curl, for example:

  CURL_ARGS="--cacert tls/ca.crt --cert client.crt --key client.key"
EOF
}

if [ "$#" -lt 2 ]; then
  usage
  exit 2
fi

out_dir="$1"
shift

mkdir -p "$out_dir"
timestamp="$(date +%Y%m%d-%H%M%S)"

index=0
for url in "$@"; do
  index=$((index + 1))
  out_file="$out_dir/orderer-${index}-${timestamp}.prom"
  # shellcheck disable=SC2086
  curl --fail --silent --show-error ${CURL_ARGS:-} "$url" > "$out_file"
  echo "$out_file"
done
