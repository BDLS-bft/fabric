# BDLS Orderer Consenter

BDLS is a Byzantine-fault-tolerant consensus protocol based on
[eprint 2019/1460](https://eprint.iacr.org/2019/1460). This package
integrates the BDLS library (`github.com/BDLS-bft/bdls`) as a third
`ConsensusType` in the Hyperledger Fabric orderer, alongside `etcdraft`
and `BFT` (smartbft).

## Quick start

1. **Generate a genesis block** with `OrdererType: BDLS`. The
   `sampleconfig/configtx.yaml` profile `SampleAppChannelBDLS` is a
   working example — it reuses the BFT consenter mapping, so a network
   that already runs smartbft can generate a BDLS channel by flipping
   `OrdererType`.

2. **Set orderer config** in `orderer.yaml`:

   ```yaml
   General:
     Cluster:
       ClientPrivateKey: /path/to/cluster-tls.key   # ECDSA P-256 PEM
       ClientCertificate: /path/to/cluster-tls.crt
   ```

   The cluster TLS keypair doubles as the BDLS signing identity.
   `ClientPrivateKey` **must** be an ECDSA key (P-256 or P-384); RSA and
   Ed25519 are rejected at startup.

3. **Start the orderer.** BDLS channels are logged under the
   `orderer.consensus.bdls` logger.

## Architecture

```
                 ┌─────────────────────────────────┐
                 │   orderer/common/server/main.go  │
                 │                                   │
                 │  smartbft.New() ──► Comm           │
                 │       │            ClusterService  │
                 │       │                │           │
                 │  bdls.New(…, Comm, ClusterSvc)     │
                 │       │                            │
                 │  multiplex handler                 │
                 │    primary: smartbft.Ingress        │
                 │    fallback: bdls.Dispatcher        │
                 └─────────────────────────────────┘
```

BDLS **shares** smartbft's cluster gRPC transport — there is only one
`ClusterNodeServiceServer` per orderer process. `main.go` installs a
multiplex handler that routes inbound `StepRequest`s to whichever
consenter owns the target channel. Outbound messages go through a
per-channel `cluster.RPC` wrapping the shared `cluster.Communicator`.

### Key files

| File | Purpose |
|---|---|
| `consenter.go` | `Consenter` struct, `New()`, `HandleChain()`, `detectSelfID`, `IsChannelMember` |
| `chain.go` | `Chain` — the per-channel run loop (`Start`/`Halt`/`Order`/`Configure`) |
| `signer.go` | Cluster TLS key loading, `makeSignDigest` closure, public-key helpers |
| `transport.go` | `peerAdapter` — adapts `cluster.RPC` to `bdlslib.PeerInterface` |
| `dispatcher.go` | Inbound message routing to per-channel chains |
| `cluster_wiring.go` | Derives `cluster.RemoteNode` + inbound auth from the last config block |
| `metadata_validator.go` | Rejects invalid config updates (bad deltas, too few consenters) |
| `blockcreator.go` | Block assembly with hash chaining |
| `blockpuller.go` | Catch-up via Fabric's deliver service, verifying `bdls_decide_proof` |
| `metrics.go` | Prometheus/StatsD counters |
| `protos/` | `ConfigMetadata`, `Consenter`, `Options`, `BDLSBlockMetadata` protobuf definitions |

## Configuration reference

### `ConfigMetadata.Options` (channel config)

All durations are in **milliseconds**. Zero means "use the library
default" (derived from `latency_ms`).

| Field | Proto | Description |
|---|---|---|
| `delta0_ms` | `int64` | Round-change stage timeout (Δ₀) |
| `delta1_ms` | `int64` | Lock stage timeout for non-leader (Δ₁) |
| `delta_prime1_ms` | `int64` | Lock stage timeout for leader (Δ'₁) |
| `delta2_ms` | `int64` | Commit stage timeout (Δ₂) |
| `delta3_ms` | `int64` | Lock-release stage timeout (Δ₃) |
| `latency_ms` | `int64` | Base network latency; used as lower bound for scheduling and as default derivation base when Δ* fields are zero |
| `reliable_decide` | `bool` | When true (default for new channels), BDLS waits for 2t+1 re-broadcasts of `<decide>` before advancing, closing the Section-9 fork window |
| `request_batch_max_count` | `uint64` | Max transactions per block (Fabric's blockcutter enforces this) |
| `request_batch_max_bytes_size` | `uint64` | Max block payload size |
| `request_batch_max_interval_ms` | `int64` | Max wait before cutting a batch |

### `ConfigMetadata.Consenters`

Each entry carries:
- `host` / `port` — cluster gRPC endpoint
- `server_tls_cert` — PEM-encoded TLS certificate; the ECDSA public key
  extracted from this cert is the BDLS participant identity
- `client_tls_cert` — PEM-encoded client TLS certificate for mTLS
- `identity` — MSP identity bytes (for Fabric-level membership; not used
  by the BDLS state machine)

### Orderer YAML

| Key | Purpose |
|---|---|
| `General.Cluster.ClientPrivateKey` | Path to the ECDSA private key PEM file whose public half matches one of the channel's `Consenters[*].ServerTlsCert` entries. This is the BDLS signing key. |
| `General.Cluster.ClientCertificate` | Corresponding TLS certificate. |

## Tuning guidance

### Δ knobs

The five delta values control how long BDLS waits at each protocol stage
before escalating (round-change, lock timeout, etc.). The paper
(Section 8.2) derives them from a single `latency` parameter:

```
Δ₀  = (round + 1) × latency
Δ₁  = (round + 1) × latency
Δ'₁ = (round + 1) × latency   (leader's lock timer)
Δ₂  = (round + 1) × latency
Δ₃  = (round + 1) × latency
```

For most LAN deployments, leaving all five at zero and setting
`latency_ms` to your p99 intra-cluster RTT (e.g. 50–200ms) works well.

Override individual deltas only when:
- **Asymmetric network**: set `delta_prime1_ms` lower than `delta1_ms`
  if the leader has a faster uplink.
- **WAN deployment**: increase `delta0_ms` and `delta3_ms` to tolerate
  higher jitter on round-change and lock-release.
- **High-throughput tuning**: lower `delta2_ms` to reduce commit latency
  at the cost of more spurious round-changes under load.

### Minimum consenter count

BDLS requires at least `3f + 1` participants to tolerate `f` Byzantine
faults. The library enforces a minimum of 4 consenters
(`ConfigMinimumParticipants`). A config update that would drop below
this threshold is rejected by the metadata validator.

## Migration

### New BDLS channel

The simplest path: create a new channel with `OrdererType: BDLS` in the
channel profile. No migration from an existing etcdraft or BFT channel
is needed.

### Switching an existing channel to BDLS

In-place `ConsensusType` migration (e.g. from `BFT` to `BDLS`) is
**not supported** in this release. BDLS uses a different participant
identity scheme (ECDSA public-key coordinates) than smartbft (MSP
identity bytes), so the two metadata shapes are not interchangeable.

The recommended path is:
1. Create a new channel with `OrdererType: BDLS`.
2. Re-submit application transactions to the new channel.
3. Decommission the old channel when ready.

## Known limitations (v1)

- **No WAL**: in-flight vote state is lost on crash. On restart the
  chain replays the current height, which is a liveness hit (not a
  safety violation). See `BDLS_V2_ROADMAP.md` in the BDLS repo for the
  planned WAL work.
- **State sync via Fabric**: catch-up uses Fabric's `BlockPuller`
  (deliver service), not a BDLS-level sync protocol.
- **Dynamic validator sets**: handled via Fabric's channel-config commit
  path (Registrar rebuilds the chain on config updates), not via an
  in-band BDLS reconfiguration record.

## Testing

```bash
# Unit tests (42 tests)
go test ./orderer/consensus/bdls/...

# Verbose
go test -v -count=1 ./orderer/consensus/bdls/...
```
