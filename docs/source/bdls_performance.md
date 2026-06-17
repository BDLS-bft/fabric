# BDLS performance baseline

This page records the initial BDLS ordering-service benchmark baseline used by
the integration roadmap. The numbers are local smoke measurements, not a
production capacity claim. They are intended to verify that the benchmark
harness works, expose the relative ordering-service behavior under identical
test-network conditions, and define the matrix that should be expanded before
making deployment recommendations.

## Benchmark harness

Run the benchmark from the repository root:

```bash
go test ./integration/bdls -run '^$' -count=1 \
  -bench BenchmarkOrderingThroughput \
  -benchtime=1x \
  -bdls.bench.consensus=all
```

The harness creates a four-orderer network for the selected consensus type,
joins peers to the benchmark channel, deploys the simple chaincode, and reports
transactions per second (`tx/s`) for submitted invokes.

Useful knobs:

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

## Local baseline

Environment: local NWO integration network, four orderers, default benchmark
batch settings unless noted, single benchmark iteration (`-benchtime=1x`).

| Case | Consensus | Payload | Batch max count | BDLS latency | Result |
| --- | --- | ---: | ---: | ---: | ---: |
| Baseline comparison | BDLS | default invoke | 500 | default | 0.5313 tx/s |
| Baseline comparison | SmartBFT (`BFT`) | default invoke | 500 | n/a | 4.771 tx/s |
| Baseline comparison | etcdraft | default invoke | 500 | n/a | 0.8602 tx/s |
| Payload comparison | BDLS | 64 bytes | 500 | default | 0.5193 tx/s |
| Payload comparison | SmartBFT (`BFT`) | 64 bytes | 500 | n/a | 5.487 tx/s |
| Payload comparison | etcdraft | 64 bytes | 500 | n/a | 0.8657 tx/s |
| BDLS timing sample | BDLS | 64 bytes | 500 | 100 ms | 0.5253 tx/s |
| BDLS batch sample | BDLS | 64 bytes | 100 | default | 0.5366 tx/s |

## Observations

* The benchmark harness can compare BDLS, SmartBFT, and etcdraft under the same
  local network shape and chaincode workload.
* In the local smoke runs, SmartBFT submitted transactions faster than BDLS and
  etcdraft. BDLS was slower than etcdraft in the single-transaction smoke
  workload.
* The 64-byte response payload did not materially change BDLS throughput in this
  small sample.
* Setting `-bdls.bench.latency=100ms` produced a similar result to the default
  BDLS timing sample. This only proves the knob is wired through and does not
  establish an optimal latency value.
* Lowering the benchmark `max-message-count` to 100 did not materially affect
  the single-transaction smoke sample. Larger `-benchtime` values and concurrent
  submission are needed to evaluate batching behavior.

## Follow-up matrix

Before treating these numbers as a performance conclusion, run multiple samples
for each point in a broader matrix:

* `-benchtime=20x`, `100x`, and higher submission counts
* payload sizes such as 0, 64, 1024, and 4096 bytes
* `max-message-count` values such as 100, 500, and 1000
* `batch-timeout` values such as 500 ms, 1 s, and 2 s
* BDLS latency and delta settings appropriate to the deployment network
* repeated runs with mean, min, max, and standard deviation

For a final throughput study, run the same matrix on dedicated hosts or
containers with controlled CPU, memory, disk, and network resources. The local
developer-machine numbers above are useful regression evidence, but they are not
capacity-planning data.
