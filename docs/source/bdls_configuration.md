# Configuring and operating a BDLS ordering service

**Audience**: *BDLS ordering service admins and performance engineers*

## Conceptual overview

BDLS is a Byzantine-fault-tolerant ordering service implementation. It runs as
the `BDLS` orderer type and uses Fabric's existing orderer cluster transport for
inter-orderer communication. BDLS channels require the `V3_0` channel capability
because block validation uses BFT-style consenter identifiers.

BDLS identifies ordering nodes through the channel `ConsenterMapping`. For block
validation, each orderer signs block metadata with an `IdentifierHeader`
containing its configured consenter ID. Peers then resolve that ID through the
channel's consenter mapping when evaluating the orderer `BlockValidation`
policy.

## Local configuration

BDLS uses the orderer's cluster TLS key pair for consensus message signing and
cluster communication. The private key must be an ECDSA key.

```yaml
General:
  Cluster:
    ClientPrivateKey: /path/to/orderer/tls/server.key
    ClientCertificate: /path/to/orderer/tls/server.crt
```

The cluster certificate in local configuration must correspond to one of the
channel's `ConsenterMapping` entries. BDLS extracts the public key from the TLS
certificate and uses it as the participant identity inside the BDLS protocol.

## Channel configuration

A BDLS channel uses `OrdererType: BDLS` and a `ConsenterMapping` with one entry
per orderer:

```yaml
Orderer:
  OrdererType: BDLS
  BatchTimeout: 1s
  BatchSize:
    MaxMessageCount: 500
    AbsoluteMaxBytes: 10 MB
    PreferredMaxBytes: 2 MB
  BDLS:
    LatencyMs: 100
    Delta0Ms: 0
    Delta1Ms: 0
    DeltaPrime1Ms: 0
    Delta2Ms: 0
    Delta3Ms: 0
    RequestBatchMaxCount: 500
    RequestBatchMaxBytesSize: 10485760
    RequestBatchMaxIntervalMs: 1000
    ReliableDecide: true
  ConsenterMapping:
  - ID: 1
    Host: orderer1.example.com
    Port: 7050
    MSPID: OrdererMSP
    Identity: /path/to/orderer1/signcert.pem
    ClientTLSCert: /path/to/orderer1/tls/server.crt
    ServerTLSCert: /path/to/orderer1/tls/server.crt
  - ID: 2
    Host: orderer2.example.com
    Port: 7050
    MSPID: OrdererMSP
    Identity: /path/to/orderer2/signcert.pem
    ClientTLSCert: /path/to/orderer2/tls/server.crt
    ServerTLSCert: /path/to/orderer2/tls/server.crt
```

`ID` values are stable numeric identifiers. Do not renumber existing orderers
when updating channel membership; block signatures are verified by resolving
these IDs through the channel configuration.

For examples, see `SampleAppChannelBDLS` in `sampleconfig/configtx.yaml` and
`nwo.MultiNodeBDLS()` in the integration test harness.

## BDLS metadata

BDLS stores per-block consensus metadata in two places:

* `BlockMetadataIndex_SIGNATURES`: Fabric block signature metadata. On `V3_0`
  channels this contains BFT-format signatures with `IdentifierHeader` values,
  allowing peers to verify blocks through `ConsenterMapping`.
* `BlockMetadataIndex_ORDERER`: BDLS operational metadata, including the decided
  BDLS height, round, and serialized decide proof.

The signed block metadata is intentionally canonical for a given block so all
orderers can aggregate signatures for the same block. The local BDLS decide
proof remains available in the orderer metadata slot for debugging and external
verification work.

## Tuning

BDLS throughput and latency are affected by the same Fabric batching knobs used
by the other ordering services:

* `BatchTimeout`: maximum wait before cutting a non-full block.
* `BatchSize.MaxMessageCount`: maximum transaction count per block.
* `BatchSize.PreferredMaxBytes`: preferred block payload size.
* `BatchSize.AbsoluteMaxBytes`: absolute block payload size limit.

BDLS also has protocol timing knobs in its consensus metadata:

* `LatencyMs` (`latency_ms`)
* `Delta0Ms` (`delta0_ms`)
* `Delta1Ms` (`delta1_ms`)
* `DeltaPrime1Ms` (`delta_prime1_ms`)
* `Delta2Ms` (`delta2_ms`)
* `Delta3Ms` (`delta3_ms`)
* `ReliableDecide` (`reliable_decide`)

For local integration networks, start with the default delta values and tune
Fabric batching first. For WAN or high-jitter networks, raise `latency_ms` or
the individual delta values before reducing `BatchTimeout`; overly aggressive
BDLS timers can cause extra round changes under load.

## Benchmarking

The BDLS integration package includes an opt-in benchmark harness:

```bash
go test ./integration/bdls -run '^$' -bench '^BenchmarkOrderingThroughput$/BDLS$' -benchtime=20x -count=1
```

The same harness can run comparable smartbft and etcdraft networks:

```bash
go test ./integration/bdls -run '^$' -bench '^BenchmarkOrderingThroughput$' -benchtime=20x -count=1 \
  -bdls.bench.consensus=all \
  -bdls.bench.batch-timeout=1s \
  -bdls.bench.max-message-count=500 \
  -bdls.bench.preferred-max-bytes-kb=512 \
  -bdls.bench.latency=100ms \
  -bdls.bench.delta0=0
```

When `-bdls.bench.payload-bytes` is greater than zero, the benchmark uses the
simple chaincode `respond` path to include a response payload of that size in
the endorsed transaction. With the default `0`, it uses the state-changing
`invoke` path.

Run each configuration multiple times and compare:

* transactions per second (`tx/s`)
* `ns/op` / latency per submitted transaction
* `BatchTimeout`
* `MaxMessageCount`
* `PreferredMaxBytes`
* BDLS delta and latency settings
* consensus type (`BDLS`, `BFT`, `etcdraft`)

Use the same chaincode, endorsement policy, block size, and transaction count
when comparing BDLS against smartbft or etcdraft.
