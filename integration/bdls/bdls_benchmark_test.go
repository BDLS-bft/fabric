/*
Copyright IBM Corp All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	ab "github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric/integration"
	"github.com/hyperledger/fabric/integration/nwo"
	"github.com/hyperledger/fabric/integration/nwo/commands"
	"github.com/hyperledger/fabric/integration/ordererclient"
	"github.com/hyperledger/fabric/protoutil"
	dcli "github.com/moby/moby/client"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

var (
	benchMode                = flag.String("bdls.bench.mode", "e2e", "benchmark mode: e2e uses peer chaincode invoke; broadcast sends signed envelopes directly to ordering service")
	benchConsensus           = flag.String("bdls.bench.consensus", "BDLS", "consensus to benchmark: BDLS, BFT, etcdraft, or all")
	benchBatchTimeout        = flag.Duration("bdls.bench.batch-timeout", time.Second, "Fabric orderer BatchTimeout used by the benchmark channel")
	benchCommitTimeout       = flag.Duration("bdls.bench.commit-timeout", 5*time.Minute, "timeout for waiting until broadcast benchmark envelopes are delivered")
	benchMaxMessageCount     = flag.Int("bdls.bench.max-message-count", 1500, "Fabric orderer BatchSize.MaxMessageCount used by the benchmark channel")
	benchAbsoluteMaxBytesMB  = flag.Int("bdls.bench.absolute-max-bytes-mb", 10, "Fabric orderer BatchSize.AbsoluteMaxBytes in MB")
	benchPreferredMaxBytesKB = flag.Int("bdls.bench.preferred-max-bytes-kb", 512, "Fabric orderer BatchSize.PreferredMaxBytes in KB")
	benchPayloadBytes        = flag.Int("bdls.bench.payload-bytes", 1, "response payload bytes for the simple chaincode respond path; 0 uses state-changing transfer")
	benchIngress             = flag.String("bdls.bench.ingress", "cluster", "broadcast ingress mode: cluster distributes client streams across orderers; single sends all streams to the first orderer")
	benchBDLSDelta0          = flag.Duration("bdls.bench.delta0", 0, "BDLS delta0 timeout; 0 uses the BDLS library default")
	benchBDLSDelta1          = flag.Duration("bdls.bench.delta1", 0, "BDLS delta1 timeout; 0 uses the BDLS library default")
	benchBDLSDeltaPrime1     = flag.Duration("bdls.bench.delta-prime1", 0, "BDLS delta-prime1 timeout; 0 uses the BDLS library default")
	benchBDLSDelta2          = flag.Duration("bdls.bench.delta2", 0, "BDLS delta2 timeout; 0 uses the BDLS library default")
	benchBDLSDelta3          = flag.Duration("bdls.bench.delta3", 0, "BDLS delta3 timeout; 0 uses the BDLS library default")
	benchBDLSLatency         = flag.Duration("bdls.bench.latency", 0, "BDLS base latency; 0 uses the BDLS library default")
	benchBDLSReliableDecide  = flag.Bool("bdls.bench.reliable-decide", true, "enable BDLS reliable decide finality gadget; false measures the original single-flood fast path")
	benchBDLSCompactState    = flag.Bool("bdls.bench.compact-state", true, "propose compact block references to BDLS and disseminate full blocks once over the cluster side channel")
	benchLogSpec             = flag.String("bdls.bench.log-spec", "orderer.consensus.bdls=info:orderer.consensus.smartbft=info:orderer.consensus.etcdraft=info", "FABRIC_LOGGING_SPEC used for benchmark orderer processes")
	benchTraceDecisions      = flag.Bool("bdls.bench.trace-decisions", false, "emit BDLS per-decision timing trace lines from benchmark orderers")
	benchMetricsDir          = flag.String("bdls.bench.metrics-dir", "", "directory for optional per-orderer Prometheus metrics snapshots")
	benchMetricsPrefix       = flag.String("bdls.bench.metrics-prefix", "", "file prefix for optional per-orderer Prometheus metrics snapshots")
	benchConcurrency         = flag.Int("bdls.bench.concurrency", 1, "number of concurrent benchmark invocations")
	benchOrderers            = flag.Int("bdls.bench.orderers", 0, "number of orderers in the benchmark network; 0 keeps the consensus default")
	benchPeersPerOrg         = flag.Int("bdls.bench.peers-per-org", 1, "number of peers per application org in the benchmark network")
)

func BenchmarkOrderingThroughput(b *testing.B) {
	gomega.RegisterTestingT(b)

	consensusTypes := requestedBenchmarkConsensus(*benchConsensus)
	for _, consensusType := range consensusTypes {
		b.Run(consensusType, func(b *testing.B) {
			runOrderingBenchmark(b, consensusType)
		})
	}
}

func requestedBenchmarkConsensus(value string) []string {
	switch strings.ToLower(value) {
	case "all":
		return []string{"BDLS", "BFT", "etcdraft"}
	case "bdls":
		return []string{"BDLS"}
	case "bft", "smartbft":
		return []string{"BFT"}
	case "etcdraft", "raft":
		return []string{"etcdraft"}
	default:
		return []string{value}
	}
}

func runOrderingBenchmark(b *testing.B, consensusType string) {
	components, shutdownBuildServer := benchmarkComponents(b)
	defer shutdownBuildServer()

	client, err := dcli.New(dcli.FromEnv)
	if err != nil {
		b.Fatalf("create docker client: %v", err)
	}

	testDir, err := os.MkdirTemp("", "bdls-ordering-benchmark")
	if err != nil {
		b.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(testDir)

	channel := "benchmarkchannel"
	config := benchmarkNetworkConfig(b, consensusType, channel)
	network := nwo.New(config, testDir, client, integration.BDLSBasePort.StartPortForNode(), components)
	network.GenerateConfigTree()
	network.Bootstrap()
	defer network.Cleanup()

	ordererProcesses := startBenchmarkOrderers(b, network)
	defer stopBenchmarkProcesses(b, ordererProcesses, network.EventuallyTimeout)

	joinBenchmarkChannel(b, network, channel)

	for _, metric := range []struct {
		name  string
		value float64
	}{
		{"batch_timeout_ms", float64(benchBatchTimeout.Milliseconds())},
		{"commit_timeout_ms", float64(benchCommitTimeout.Milliseconds())},
		{"max_message_count", float64(*benchMaxMessageCount)},
		{"absolute_max_bytes_mb", float64(*benchAbsoluteMaxBytesMB)},
		{"preferred_max_bytes_kb", float64(*benchPreferredMaxBytesKB)},
		{"payload_bytes", float64(*benchPayloadBytes)},
		{"single_ingress", boolMetric(strings.EqualFold(*benchIngress, "single"))},
		{"bdls_delta0_ms", float64(benchBDLSDelta0.Milliseconds())},
		{"bdls_delta1_ms", float64(benchBDLSDelta1.Milliseconds())},
		{"bdls_delta_prime1_ms", float64(benchBDLSDeltaPrime1.Milliseconds())},
		{"bdls_delta2_ms", float64(benchBDLSDelta2.Milliseconds())},
		{"bdls_delta3_ms", float64(benchBDLSDelta3.Milliseconds())},
		{"bdls_latency_ms", float64(benchBDLSLatency.Milliseconds())},
		{"bdls_reliable_decide", boolMetric(*benchBDLSReliableDecide)},
		{"bdls_compact_state", boolMetric(*benchBDLSCompactState)},
		{"concurrency", float64(*benchConcurrency)},
		{"orderers", float64(len(network.Orderers))},
		{"peers", float64(len(network.Peers))},
	} {
		b.ReportMetric(metric.value, metric.name)
	}

	switch strings.ToLower(*benchMode) {
	case "e2e":
		peerProcesses := startBenchmarkPeers(b, network)
		defer stopBenchmarkProcess(b, peerProcesses, network.EventuallyTimeout)

		network.JoinChannel(channel, network.Orderers[0], network.PeersWithChannel(channel)...)
		waitForBenchmarkChannelActive(b, network, network.Orderers, channel)
		deployBenchmarkChaincode(network, channel, testDir, components)

		peer := network.Peer("Org1", "peer0")
		b.ResetTimer()
		started := time.Now()
		if *benchConcurrency <= 1 {
			for i := 0; i < b.N; i++ {
				err := benchmarkInvoke(network, peer, network.Orderers[i%len(network.Orderers)], channel)
				if err != nil {
					b.Fatal(err)
				}
			}
		} else {
			runConcurrentOrderingBenchmark(b, network, peer, network.Orderers, channel)
		}
		elapsed := time.Since(started)
		b.StopTimer()
		if elapsed > 0 {
			b.ReportMetric(float64(b.N)/elapsed.Seconds(), "tx/s")
		}
	case "broadcast":
		runBroadcastOrderingBenchmark(b, network, network.Orderers, channel)
	default:
		b.Fatalf("unsupported benchmark mode %q", *benchMode)
	}
	dumpBenchmarkOrdererMetrics(b, network, consensusType)
}

func runConcurrentOrderingBenchmark(b *testing.B, network *nwo.Network, peer *nwo.Peer, orderers []*nwo.Orderer, channel string) {
	b.Helper()

	if len(orderers) == 0 {
		b.Fatal("no orderers available")
	}

	workerCount := *benchConcurrency
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > b.N {
		workerCount = b.N
	}

	jobs := make(chan int, workerCount)
	errs := make(chan error, workerCount)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for txID := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				err := benchmarkInvoke(network, peer, orderers[txID%len(orderers)], channel)
				if err != nil {
					select {
					case errs <- fmt.Errorf("worker %d: %w", workerID, err):
					default:
					}
					cancel()
					return
				}
			}
		}(i)
	}

	for i := 0; i < b.N; i++ {
		select {
		case <-ctx.Done():
			goto stopQueueing
		case jobs <- i:
		}
	}

stopQueueing:
	close(jobs)

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			b.Fatal(err)
		}
	}
}

func runBroadcastOrderingBenchmark(b *testing.B, network *nwo.Network, orderers []*nwo.Orderer, channel string) {
	b.Helper()

	if len(orderers) == 0 {
		b.Fatal("no orderers available")
	}

	workerCount := *benchConcurrency
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > b.N {
		workerCount = b.N
	}

	ingressOrderers := benchmarkIngressOrderers(b, orderers)
	envs := make([]*common.Envelope, b.N)
	for i := range envs {
		envs[i] = ordererclient.CreateBroadcastEnvelope(network, ingressOrderers[i%len(ingressOrderers)], channel, benchmarkBroadcastPayload(i), common.HeaderType_ENDORSER_TRANSACTION)
	}

	startBlockNum := waitForBroadcastClusterReady(b, network, orderers, channel)

	jobs := make(chan *common.Envelope, workerCount)
	errs := make(chan error, workerCount)
	startC := make(chan struct{})
	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()
	var sentEnvelopes uint64

	var ready sync.WaitGroup
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workerOrderer := ingressOrderers[i%len(ingressOrderers)]
		ready.Add(1)
		wg.Add(1)
		go func(workerID int, orderer *nwo.Orderer) {
			defer wg.Done()
			conn, err := benchmarkOrdererClientConn(network, orderer)
			if err != nil {
				select {
				case errs <- fmt.Errorf("worker %d: create orderer client connection: %w", workerID, err):
				default:
				}
				sendCancel()
				ready.Done()
				return
			}
			defer conn.Close()

			workerCtx, workerCancel := context.WithCancel(sendCtx)
			defer workerCancel()

			broadcaster, err := ab.NewAtomicBroadcastClient(conn).Broadcast(workerCtx)
			if err != nil {
				select {
				case errs <- fmt.Errorf("worker %d: create broadcast stream: %w", workerID, err):
				default:
				}
				sendCancel()
				ready.Done()
				return
			}
			defer broadcaster.CloseSend()

			ready.Done()
			<-startC

			for env := range jobs {
				select {
				case <-sendCtx.Done():
					return
				default:
				}
				if err := broadcaster.Send(env); err != nil {
					select {
					case errs <- fmt.Errorf("worker %d: send envelope: %w", workerID, err):
					default:
					}
					sendCancel()
					return
				}
				resp, err := broadcaster.Recv()
				if err != nil {
					select {
					case errs <- fmt.Errorf("worker %d: receive broadcast response: %w", workerID, err):
					default:
					}
					sendCancel()
					return
				}
				if resp == nil {
					select {
					case errs <- fmt.Errorf("worker %d: receive broadcast response: nil response", workerID):
					default:
					}
					sendCancel()
					return
				}
				if resp.GetStatus() != common.Status_SUCCESS {
					select {
					case errs <- fmt.Errorf("worker %d: broadcast status %s", workerID, resp.GetStatus()):
					default:
					}
					sendCancel()
					return
				}
				atomic.AddUint64(&sentEnvelopes, 1)
			}
		}(i, workerOrderer)
	}

	ready.Wait()
	b.ResetTimer()
	started := time.Now()
	close(startC)
	sendStarted := time.Now()
sendLoop:
	for _, env := range envs {
		select {
		case jobs <- env:
		case <-sendCtx.Done():
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
	sendElapsed := time.Since(sendStarted)
	close(errs)

	for err := range errs {
		if err != nil {
			b.Fatal(err)
		}
	}
	commitWaitStarted := time.Now()
	sentCount := int(atomic.LoadUint64(&sentEnvelopes))
	if sentCount == 0 {
		b.Fatal("broadcast benchmark failed to send any envelopes")
	}
	nextBlockNum := waitForBroadcastCommitsFrom(b, network, orderers, channel, startBlockNum, sentCount)
	commitWaitElapsed := time.Since(commitWaitStarted)
	elapsed := time.Since(started)
	b.StopTimer()

	if elapsed > 0 {
		b.ReportMetric(float64(sentCount)/elapsed.Seconds(), "tx/s")
	}
	if sendElapsed > 0 {
		b.ReportMetric(float64(sentCount)/sendElapsed.Seconds(), "send_tx/s")
		b.ReportMetric(sendElapsed.Seconds(), "send_s")
	}
	if commitWaitElapsed > 0 {
		b.ReportMetric(commitWaitElapsed.Seconds(), "commit_wait_s")
	}
	blocks := nextBlockNum - startBlockNum
	if blocks > 0 {
		b.ReportMetric(float64(blocks), "blocks")
		b.ReportMetric(float64(sentCount)/float64(blocks), "tx/block")
		b.ReportMetric(float64(blocks)/elapsed.Seconds(), "block/s")
	}
}

func benchmarkIngressOrderers(b *testing.B, orderers []*nwo.Orderer) []*nwo.Orderer {
	b.Helper()

	switch strings.ToLower(*benchIngress) {
	case "cluster":
		return orderers
	case "single":
		return orderers[:1]
	default:
		b.Fatalf("unsupported benchmark ingress mode %q", *benchIngress)
		return nil
	}
}

func boolMetric(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func dumpBenchmarkOrdererMetrics(b *testing.B, network *nwo.Network, consensusType string) {
	b.Helper()
	if strings.TrimSpace(*benchMetricsDir) == "" {
		return
	}
	if err := os.MkdirAll(*benchMetricsDir, 0o755); err != nil {
		b.Fatalf("create metrics directory: %v", err)
	}

	prefix := sanitizeMetricsFilename(*benchMetricsPrefix)
	if prefix == "" {
		prefix = sanitizeMetricsFilename(b.Name())
	}
	if prefix == "" {
		prefix = sanitizeMetricsFilename(consensusType)
	}

	for _, orderer := range network.Orderers {
		client, _ := nwo.OrdererOperationalClients(network, orderer)
		client.Timeout = 10 * time.Second
		url := "https://" + network.OrdererAddress(orderer, nwo.OperationsPort) + "/metrics"
		resp, err := client.Get(url)
		if err != nil {
			b.Fatalf("scrape orderer metrics from %s: %v", orderer.ID(), err)
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			b.Fatalf("read orderer metrics from %s: %v", orderer.ID(), readErr)
		}
		if closeErr != nil {
			b.Fatalf("close orderer metrics response from %s: %v", orderer.ID(), closeErr)
		}
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("scrape orderer metrics from %s: %s", orderer.ID(), resp.Status)
		}
		path := filepath.Join(*benchMetricsDir, prefix+"."+sanitizeMetricsFilename(orderer.ID())+".prom")
		if err := os.WriteFile(path, body, 0o644); err != nil {
			b.Fatalf("write orderer metrics snapshot %s: %v", path, err)
		}
	}
}

func sanitizeMetricsFilename(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func benchmarkComponents(b *testing.B) (*nwo.Components, func()) {
	b.Helper()
	if components != nil {
		return components, func() {}
	}

	server := nwo.NewBuildServer()
	if err := server.Serve(); err != nil {
		b.Fatalf("failed to start benchmark build server: %v", err)
	}
	return server.Components(), server.Shutdown
}

func benchmarkNetworkConfig(b *testing.B, consensusType, channel string) *nwo.Config {
	b.Helper()
	var config *nwo.Config
	switch consensusType {
	case "BDLS":
		config = nwo.MultiNodeBDLS()
	case "BFT":
		config = nwo.MultiNodeSmartBFT()
	case "etcdraft":
		config = nwo.MultiNodeEtcdRaft()
	default:
		b.Fatalf("unsupported benchmark consensus type %q", consensusType)
	}

	resizeBenchmarkNetworkConfig(b, config, consensusType, channel)

	config.Channels = nil
	for _, profile := range config.Profiles {
		if len(profile.AppCapabilities) == 0 {
			profile.AppCapabilities = []string{"V2_0"}
		}
		profile.Blocks = &nwo.Blocks{
			BatchTimeout:      int(benchBatchTimeout.Round(time.Second) / time.Second),
			MaxMessageCount:   *benchMaxMessageCount,
			AbsoluteMaxBytes:  *benchAbsoluteMaxBytesMB,
			PreferredMaxBytes: *benchPreferredMaxBytesKB,
		}
		if consensusType == "BDLS" {
			profile.BDLS = &nwo.BDLS{
				Delta0Ms:                  int(benchBDLSDelta0.Milliseconds()),
				Delta1Ms:                  int(benchBDLSDelta1.Milliseconds()),
				DeltaPrime1Ms:             int(benchBDLSDeltaPrime1.Milliseconds()),
				Delta2Ms:                  int(benchBDLSDelta2.Milliseconds()),
				Delta3Ms:                  int(benchBDLSDelta3.Milliseconds()),
				LatencyMs:                 int(benchBDLSLatency.Milliseconds()),
				RequestBatchMaxCount:      *benchMaxMessageCount,
				RequestBatchMaxBytesSize:  *benchAbsoluteMaxBytesMB * 1024 * 1024,
				RequestBatchMaxIntervalMs: int(benchBatchTimeout.Milliseconds()),
				ReliableDecide:            *benchBDLSReliableDecide,
				DisableReliableDecide:     !*benchBDLSReliableDecide,
				CompactBlockState:         *benchBDLSCompactState,
			}
		}
		if consensusType == "BFT" {
			smartBFT := profile.SmartBFT
			if smartBFT == nil {
				smartBFT = &nwo.SmartBFT{}
			}
			smartBFT.RequestBatchMaxCount = *benchMaxMessageCount
			smartBFT.RequestBatchMaxBytes = *benchAbsoluteMaxBytesMB * 1024 * 1024
			smartBFT.RequestBatchMaxInterval = benchBatchTimeout.String()
			smartBFT.RequestPoolSize = *benchMaxMessageCount * 4
			if smartBFT.RequestPoolSize < 400 {
				smartBFT.RequestPoolSize = 400
			}
			if smartBFT.LeaderHeartbeatTimeout == 0 {
				smartBFT.LeaderHeartbeatTimeout = 60
			}
			if smartBFT.LeaderHeartbeatCount == 0 {
				smartBFT.LeaderHeartbeatCount = 10
			}
			profile.SmartBFT = smartBFT
		}
	}
	for _, peer := range config.Peers {
		peer.Channels = []*nwo.PeerChannel{{Name: channel, Anchor: peer.Name == "peer0"}}
	}
	return config
}

func resizeBenchmarkNetworkConfig(b *testing.B, config *nwo.Config, consensusType, channel string) {
	b.Helper()

	if *benchOrderers > 0 {
		if *benchOrderers < 1 {
			b.Fatalf("orderer count must be positive, got %d", *benchOrderers)
		}
		if (consensusType == "BDLS" || consensusType == "BFT") && *benchOrderers < 4 {
			b.Fatalf("%s benchmark requires at least 4 orderers, got %d", consensusType, *benchOrderers)
		}

		ordererNames := make([]string, 0, *benchOrderers)
		config.Orderers = make([]*nwo.Orderer, 0, *benchOrderers)
		for i := 1; i <= *benchOrderers; i++ {
			name := fmt.Sprintf("orderer%d", i)
			config.Orderers = append(config.Orderers, &nwo.Orderer{
				Name:         name,
				Organization: "OrdererOrg",
				Id:           i,
			})
			ordererNames = append(ordererNames, name)
		}
		for _, profile := range config.Profiles {
			profile.Orderers = ordererNames
		}
	}

	if *benchPeersPerOrg < 1 {
		b.Fatalf("peers-per-org must be positive, got %d", *benchPeersPerOrg)
	}
	if *benchPeersPerOrg == 1 {
		return
	}

	peerChannels := []*nwo.PeerChannel{{Name: channel, Anchor: false}}
	config.Peers = nil
	for _, orgName := range benchmarkApplicationOrgs(config) {
		for i := 0; i < *benchPeersPerOrg; i++ {
			channels := peerChannels
			if i == 0 {
				channels = []*nwo.PeerChannel{{Name: channel, Anchor: true}}
			}
			config.Peers = append(config.Peers, &nwo.Peer{
				Name:         fmt.Sprintf("peer%d", i),
				Organization: orgName,
				Channels:     channels,
			})
		}
	}
}

func benchmarkApplicationOrgs(config *nwo.Config) []string {
	seen := map[string]struct{}{}
	orgs := []string{}
	for _, profile := range config.Profiles {
		for _, orgName := range profile.Organizations {
			if _, ok := seen[orgName]; ok {
				continue
			}
			seen[orgName] = struct{}{}
			orgs = append(orgs, orgName)
		}
	}
	return orgs
}

func startBenchmarkOrderers(b *testing.B, network *nwo.Network) []ifrit.Process {
	b.Helper()
	logSpec := benchmarkOrdererLogSpec()
	traceDecisions := *benchTraceDecisions || strings.Contains(logSpec, "orderer.consensus.bdls.trace")
	if traceDecisions {
		b.Setenv("FABRIC_BDLS_TRACE_DECISIONS", "true")
	}

	var processes []ifrit.Process
	for _, orderer := range network.Orderers {
		env := []string{}
		if logSpec != "" {
			env = append(env, "FABRIC_LOGGING_SPEC="+logSpec)
		}
		if traceDecisions {
			env = append(env, "FABRIC_BDLS_TRACE_DECISIONS=true")
		}
		runner := network.OrdererRunner(orderer, env...)
		for _, value := range env {
			key, envValue, ok := strings.Cut(value, "=")
			if ok {
				runner.Command.Env = setEnvValue(runner.Command.Env, key, envValue)
			}
		}
		proc := ifrit.Invoke(runner)
		processes = append(processes, proc)
		gomega.Eventually(proc.Ready(), network.EventuallyTimeout).Should(gomega.BeClosed())
	}
	return processes
}

func benchmarkOrdererLogSpec() string {
	spec := strings.TrimSpace(*benchLogSpec)
	if spec == "" {
		return ""
	}
	// nwo.OrdererRunner marks a process ready by watching for the
	// "Beginning to serve requests" line from orderer.common.server. Keep that
	// module at info even when a benchmark suppresses other noisy components.
	return spec + ":orderer.common.server=info"
}

func setEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	filtered := env[:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, prefix+value)
}

func startBenchmarkPeers(b *testing.B, network *nwo.Network) ifrit.Process {
	b.Helper()
	group := benchmarkPeerGroupRunner(network)
	process := ifrit.Invoke(group)
	gomega.Eventually(process.Ready(), network.EventuallyTimeout).Should(gomega.BeClosed())
	return process
}

func benchmarkPeerGroupRunner(network *nwo.Network) ifrit.Runner {
	members := grouper.Members{}
	for _, peer := range network.Peers {
		runner := network.PeerRunner(peer)
		runner.Command.Env = append(runner.Command.Env, "FABRIC_LOGGING_SPEC=info")
		members = append(members, grouper.Member{Name: peer.ID(), Runner: runner})
	}
	return grouper.NewParallel(syscall.SIGTERM, members)
}

func stopBenchmarkProcesses(b *testing.B, processes []ifrit.Process, timeout time.Duration) {
	b.Helper()
	for _, proc := range processes {
		if proc == nil {
			continue
		}
		stopProcess(proc, timeout)
	}
}

func stopBenchmarkProcess(b *testing.B, process ifrit.Process, timeout time.Duration) {
	b.Helper()
	if process == nil {
		return
	}
	stopProcess(process, timeout)
}

func joinBenchmarkChannel(b *testing.B, network *nwo.Network, channel string) {
	b.Helper()
	genesisBlockBytes, err := os.ReadFile(network.OutputBlockPath(channel))
	if err != nil && errors.Is(err, syscall.ENOENT) {
		sess, err := network.ConfigTxGen(commands.OutputBlock{
			ChannelID:   channel,
			Profile:     network.Profiles[0].Name,
			ConfigPath:  network.RootDir,
			OutputBlock: network.OutputBlockPath(channel),
		})
		if err != nil {
			b.Fatalf("create configtxgen session: %v", err)
		}
		gomega.Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))

		genesisBlockBytes, err = os.ReadFile(network.OutputBlockPath(channel))
		if err != nil {
			b.Fatalf("read generated genesis block: %v", err)
		}
	} else if err != nil {
		b.Fatalf("read genesis block: %v", err)
	}

	genesisBlock := &common.Block{}
	if err := proto.Unmarshal(genesisBlockBytes, genesisBlock); err != nil {
		b.Fatalf("unmarshal genesis block: %v", err)
	}

	expectedChannelInfoPT := nwo.ChannelInfo{
		Name:              channel,
		Status:            "active",
		ConsensusRelation: "consenter",
		Height:            1,
	}

	blockBytes, err := proto.Marshal(genesisBlock)
	if err != nil {
		b.Fatalf("marshal genesis block: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(network.Orderers))
	for _, orderer := range network.Orderers {
		wg.Add(1)
		go func(orderer *nwo.Orderer) {
			defer wg.Done()
			if err := joinBenchmarkOrdererWithRetry(network, orderer, channel, blockBytes, expectedChannelInfoPT); err != nil {
				errs <- err
			}
		}(orderer)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			b.Fatal(err)
		}
	}
	waitForBenchmarkChannelActive(b, network, network.Orderers, channel)
}

func joinBenchmarkOrdererWithRetry(network *nwo.Network, orderer *nwo.Orderer, channel string, blockBytes []byte, expectedChannelInfo nwo.ChannelInfo) error {
	var lastErr error
	deadline := time.Now().Add(network.EventuallyTimeout)
	for attempt := 1; ; attempt++ {
		lastErr = joinBenchmarkOrderer(network, orderer, channel, blockBytes, expectedChannelInfo)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("joining orderer %s to channel %s did not succeed within %s: %w", orderer.ID(), channel, network.EventuallyTimeout, lastErr)
		}
		delay := time.Duration(attempt) * 250 * time.Millisecond
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

func joinBenchmarkOrderer(network *nwo.Network, orderer *nwo.Orderer, channel string, blockBytes []byte, expectedChannelInfo nwo.ChannelInfo) error {
	protocol := "http"
	if network.TLSEnabled {
		protocol = "https"
	}
	url := fmt.Sprintf("%s://127.0.0.1:%d/participation/v1/channels", protocol, network.OrdererPort(orderer, nwo.AdminPort))
	req := nwo.GenerateJoinRequest(url, channel, blockBytes)
	authClient, unauthClient := nwo.OrdererOperationalClients(network, orderer)

	client := unauthClient
	if network.TLSEnabled {
		client = authClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), ordererAdminRequestTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("join channel on %s: %w", orderer.ID(), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("join channel on %s: read response: %w", orderer.ID(), err)
	}
	bodyText := strings.TrimSpace(string(body))
	alreadyJoined := (resp.StatusCode == http.StatusBadRequest ||
		resp.StatusCode == http.StatusConflict ||
		resp.StatusCode == http.StatusMethodNotAllowed) &&
		(strings.Contains(strings.ToLower(bodyText), "already") || strings.Contains(strings.ToLower(bodyText), "exists"))
	if resp.StatusCode != http.StatusCreated && !alreadyJoined {
		return fmt.Errorf("join channel on %s: status %d: %s", orderer.ID(), resp.StatusCode, bodyText)
	}
	if alreadyJoined {
		return nil
	}
	if len(bodyText) == 0 {
		return nil
	}
	var channelInfo nwo.ChannelInfo
	if err := json.Unmarshal(body, &channelInfo); err != nil {
		return fmt.Errorf("join channel on %s: decode response: %w", orderer.ID(), err)
	}
	if channelInfo.Name != expectedChannelInfo.Name {
		return fmt.Errorf("join channel on %s: channel name mismatch: got %q want %q", orderer.ID(), channelInfo.Name, expectedChannelInfo.Name)
	}
	if channelInfo.Status != expectedChannelInfo.Status {
		return fmt.Errorf("join channel on %s: status mismatch: got %q want %q", orderer.ID(), channelInfo.Status, expectedChannelInfo.Status)
	}
	if channelInfo.ConsensusRelation != expectedChannelInfo.ConsensusRelation {
		return fmt.Errorf("join channel on %s: consensus relation mismatch: got %q want %q", orderer.ID(), channelInfo.ConsensusRelation, expectedChannelInfo.ConsensusRelation)
	}
	if channelInfo.Height < uint64(expectedChannelInfo.Height) {
		return fmt.Errorf("join channel on %s: height too low: got %d want >= %d", orderer.ID(), channelInfo.Height, expectedChannelInfo.Height)
	}
	return nil
}

func deployBenchmarkChaincode(network *nwo.Network, channel string, testDir string, components *nwo.Components) {
	nwo.DeployChaincode(network, channel, network.Orderers[0], nwo.Chaincode{
		Name:            "mycc",
		Version:         "0.0",
		Path:            components.Build("github.com/hyperledger/fabric/integration/chaincode/simple/cmd"),
		Lang:            "binary",
		PackageFile:     filepath.Join(testDir, "simplecc.tar.gz"),
		Ctor:            `{"Args":["init","a","100","b","200"]}`,
		SignaturePolicy: `AND ('Org1MSP.member','Org2MSP.member')`,
		Sequence:        "1",
		InitRequired:    true,
		Label:           "my_prebuilt_chaincode",
	})
}

func benchmarkInvoke(network *nwo.Network, peer *nwo.Peer, orderer *nwo.Orderer, channel string) error {
	sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeInvoke{
		ChannelID: channel,
		Orderer:   network.OrdererAddress(orderer, nwo.ListenPort),
		Name:      "mycc",
		Ctor:      benchmarkCtor(*benchPayloadBytes),
		PeerAddresses: []string{
			network.PeerAddress(network.Peer("Org1", "peer0"), nwo.ListenPort),
			network.PeerAddress(network.Peer("Org2", "peer0"), nwo.ListenPort),
		},
		WaitForEvent: true,
	})
	if err != nil {
		return fmt.Errorf("create invoke session: %w", err)
	}

	gomega.Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
	if sess.ExitCode() != 0 {
		return fmt.Errorf("chaincode invoke failed with exit code %d: %s", sess.ExitCode(), strings.TrimSpace(string(sess.Err.Contents())))
	}
	if !bytes.Contains(sess.Err.Contents(), []byte("Chaincode invoke successful. result: status:200")) {
		return fmt.Errorf("chaincode invoke missing success output: %s", strings.TrimSpace(string(sess.Err.Contents())))
	}

	return nil
}

func benchmarkCtor(payloadBytes int) string {
	if payloadBytes <= 0 {
		return `{"Args":["invoke","a","b","1"]}`
	}
	return fmt.Sprintf(`{"Args":["respond","200","ok","%s"]}`, strings.Repeat("x", payloadBytes))
}

func benchmarkBroadcastPayload(i int) []byte {
	size := *benchPayloadBytes
	if size < 1 {
		size = 1
	}
	payload := make([]byte, size)
	copy(payload, fmt.Appendf(nil, "tx-%d", i))
	return payload
}

func waitForBroadcastClusterReady(b *testing.B, network *nwo.Network, orderers []*nwo.Orderer, channel string) uint64 {
	b.Helper()

	if len(orderers) == 0 {
		b.Fatal("no orderers available")
	}

	errs := make(chan error, len(orderers))
	for i, orderer := range orderers {
		payload := fmt.Appendf(nil, "warmup-%d", i)
		go func(orderer *nwo.Orderer, payload []byte) {
			errs <- waitForBroadcastReady(b, network, orderer, channel, payload)
		}(orderer, payload)
	}
	for i := 0; i < len(orderers); i++ {
		if err := <-errs; err != nil {
			b.Fatal(err)
		}
	}

	nextBlockNum := waitForBroadcastCommitsFrom(b, network, orderers, channel, 1, len(orderers))
	waitForBroadcastClusterHeight(b, network, orderers, channel, nextBlockNum)
	return nextBlockNum
}

func waitForBroadcastReady(b *testing.B, network *nwo.Network, orderer *nwo.Orderer, channel string, payload []byte) error {
	b.Helper()

	timeout := network.EventuallyTimeout
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		env := ordererclient.CreateBroadcastEnvelope(network, orderer, channel, payload, common.HeaderType_ENDORSER_TRANSACTION)
		if err := broadcastBenchmarkEnvelope(b, network, orderer, env); err != nil {
			lastErr = err
			delay := 250 * time.Millisecond
			if remaining := time.Until(deadline); remaining < delay {
				return fmt.Errorf("ordering service %s was not ready for broadcast within %s: %v", orderer.ID(), timeout, lastErr)
			}
			time.Sleep(delay)
			continue
		}
		return nil
	}
	return fmt.Errorf("ordering service %s was not ready for broadcast within %s: %v", orderer.ID(), timeout, lastErr)
}

func broadcastBenchmarkEnvelope(b *testing.B, network *nwo.Network, orderer *nwo.Orderer, env *common.Envelope) error {
	b.Helper()

	conn, err := benchmarkOrdererClientConn(network, orderer)
	if err != nil {
		return fmt.Errorf("create orderer client connection: %w", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), benchmarkBroadcastTimeout())
	defer cancel()

	broadcaster, err := ab.NewAtomicBroadcastClient(conn).Broadcast(ctx)
	if err != nil {
		return fmt.Errorf("create broadcast stream: %w", err)
	}
	defer broadcaster.CloseSend()
	if err := broadcaster.Send(env); err != nil {
		return fmt.Errorf("send envelope: %w", err)
	}
	resp, err := broadcaster.Recv()
	if err != nil {
		return fmt.Errorf("receive broadcast response: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("receive broadcast response: nil response")
	}
	if resp.GetStatus() != common.Status_SUCCESS {
		return fmt.Errorf("broadcast status %s", resp.GetStatus())
	}
	return nil
}

func benchmarkBroadcastTimeout() time.Duration {
	timeout := *benchCommitTimeout
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	if timeout < time.Second {
		timeout = time.Second
	}
	return timeout
}

func waitForBroadcastCommitsFrom(b *testing.B, network *nwo.Network, orderers []*nwo.Orderer, channel string, startBlockNum uint64, txCount int) uint64 {
	b.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), *benchCommitTimeout)
	defer cancel()

	committed := 0
	for blockNum := startBlockNum; committed < txCount; blockNum++ {
		block, err := deliverBroadcastBlockWithFallback(b, ctx, network, orderers, channel, blockNum)
		if err != nil {
			b.Fatalf("deliver block %d while waiting for %d broadcast commits after %d committed envelopes: %v",
				blockNum, txCount, committed, err)
		}
		if block.Data != nil {
			committed += len(block.Data.Data)
		}
		if committed >= txCount {
			return blockNum + 1
		}
	}
	return startBlockNum
}

func waitForBroadcastClusterHeight(b *testing.B, network *nwo.Network, orderers []*nwo.Orderer, channel string, nextBlockNum uint64) {
	b.Helper()

	if nextBlockNum == 0 {
		return
	}
	lastBlockNum := nextBlockNum - 1
	ctx, cancel := context.WithTimeout(context.Background(), network.EventuallyTimeout)
	defer cancel()

	errs := make(chan error, len(orderers))
	for _, orderer := range orderers {
		go func(orderer *nwo.Orderer) {
			if _, err := deliverBenchmarkBlockWithRetry(b, ctx, network, orderer, channel, lastBlockNum); err != nil {
				errs <- fmt.Errorf("orderer %s did not deliver warmup block %d before benchmark start: %w", orderer.ID(), lastBlockNum, err)
				return
			}
			errs <- nil
		}(orderer)
	}
	for i := 0; i < len(orderers); i++ {
		if err := <-errs; err != nil {
			b.Fatal(err)
		}
	}
}

func deliverBroadcastBlockWithFallback(
	b *testing.B,
	ctx context.Context,
	network *nwo.Network,
	orderers []*nwo.Orderer,
	channel string,
	blockNum uint64,
) (*common.Block, error) {
	b.Helper()

	if len(orderers) == 0 {
		return nil, fmt.Errorf("no orderers configured for broadcast block delivery")
	}

	fallbackCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type deliverResult struct {
		block *common.Block
		err   error
	}
	results := make(chan deliverResult, len(orderers))

	for _, orderer := range orderers {
		go func(orderer *nwo.Orderer) {
			block, err := deliverBenchmarkBlockWithRetry(b, fallbackCtx, network, orderer, channel, blockNum)
			results <- deliverResult{block: block, err: err}
			if err == nil {
				cancel()
			}
		}(orderer)
	}

	lastErr := error(nil)
	for i := 0; i < len(orderers); i++ {
		res := <-results
		if res.err == nil {
			return res.block, nil
		}
		lastErr = res.err
	}
	return nil, lastErr
}

func deliverBenchmarkBlockWithRetry(b *testing.B, ctx context.Context, network *nwo.Network, orderer *nwo.Orderer, channel string, blockNum uint64) (*common.Block, error) {
	b.Helper()

	var lastErr error
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Time{}
	}
	for attempt := 1; ; attempt++ {
		block, err := deliverBenchmarkBlock(b, ctx, network, orderer, channel, blockNum)
		if err == nil {
			return block, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("orderer %s did not deliver block %d before context deadline: %w", orderer.ID(), blockNum, ctx.Err())
		default:
		}
		b.Logf("attempt %d: failed to deliver block %d from orderer %s: %v; retrying", attempt, blockNum, orderer.ID(), err)
		delay := time.Duration(attempt) * 250 * time.Millisecond
		if delay > 2*time.Second {
			delay = 2 * time.Second
		}
		if hasDeadline {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return nil, fmt.Errorf("orderer %s did not deliver block %d within deadline: %w", orderer.ID(), blockNum, lastErr)
			}
			if remaining < delay {
				delay = remaining
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("orderer %s did not deliver block %d before context deadline: %w", orderer.ID(), blockNum, ctx.Err())
		case <-time.After(delay):
		}
	}
}

func deliverBenchmarkBlock(b *testing.B, ctx context.Context, network *nwo.Network, orderer *nwo.Orderer, channel string, blockNum uint64) (*common.Block, error) {
	b.Helper()

	specified := &ab.SeekPosition{
		Type: &ab.SeekPosition_Specified{
			Specified: &ab.SeekSpecified{Number: blockNum},
		},
	}
	env, err := protoutil.CreateSignedEnvelope(
		common.HeaderType_DELIVER_SEEK_INFO,
		channel,
		network.OrdererUserSigner(orderer, "Admin"),
		&ab.SeekInfo{
			Start:    specified,
			Stop:     specified,
			Behavior: ab.SeekInfo_BLOCK_UNTIL_READY,
		},
		0,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("create signed deliver envelope on orderer %s for block %d on channel %s: %w", orderer.ID(), blockNum, channel, err)
	}

	conn, err := benchmarkOrdererClientConn(network, orderer)
	if err != nil {
		return nil, fmt.Errorf("orderer %s: create orderer client connection for block %d: %w", orderer.ID(), blockNum, err)
	}
	defer conn.Close()

	deliverer, err := ab.NewAtomicBroadcastClient(conn).Deliver(ctx)
	if err != nil {
		return nil, fmt.Errorf("orderer %s: create deliver stream for block %d: %w", orderer.ID(), blockNum, err)
	}
	defer deliverer.CloseSend()
	if err := deliverer.Send(env); err != nil {
		return nil, fmt.Errorf("orderer %s: send deliver request for block %d: %w", orderer.ID(), blockNum, err)
	}
	resp, err := deliverer.Recv()
	if err != nil {
		return nil, fmt.Errorf("orderer %s: receive deliver response for block %d: %w", orderer.ID(), blockNum, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("orderer %s: deliver response for block %d was nil", orderer.ID(), blockNum)
	}
	block := resp.GetBlock()
	if block != nil {
		return block, nil
	}
	if status := resp.GetStatus(); status != common.Status_SUCCESS {
		return nil, fmt.Errorf("orderer %s: deliver returned status %s for block %d", orderer.ID(), status, blockNum)
	}
	return nil, fmt.Errorf("orderer %s: deliver response for block %d missing block payload", orderer.ID(), blockNum)
}

func waitForBenchmarkChannelActive(b *testing.B, network *nwo.Network, orderers []*nwo.Orderer, channel string) {
	b.Helper()

	if len(orderers) == 0 {
		b.Fatal("no orderers available")
	}

	errs := make(chan error, len(orderers))
	for _, orderer := range orderers {
		o := orderer
		go func() {
			errs <- waitForOrdererChannelActive(o, network, channel, network.EventuallyTimeout)
		}()
	}
	for range orderers {
		if err := <-errs; err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkOrdererClientConn(network *nwo.Network, orderer *nwo.Orderer) (*grpc.ClientConn, error) {
	maxRecvBytes := *benchAbsoluteMaxBytesMB * 2 * 1024 * 1024
	if maxRecvBytes < 16*1024*1024 {
		maxRecvBytes = 16 * 1024 * 1024
	}
	dialTimeout := network.EventuallyTimeout
	if dialTimeout > 30*time.Second {
		dialTimeout = 30 * time.Second
	}
	if dialTimeout < 2*time.Second {
		dialTimeout = 2 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	dialAddr := network.OrdererAddress(orderer, nwo.ListenPort)
	if dialAddr == "" {
		return nil, fmt.Errorf("orderer %s has empty listen address", orderer.ID())
	}

	options := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxRecvBytes)),
		grpc.WithBlock(),
	}
	if network.TLSEnabled {
		creds, err := credentials.NewClientTLSFromFile(filepath.Join(network.OrdererLocalTLSDir(orderer), "ca.crt"), "")
		if err != nil {
			return nil, fmt.Errorf("create orderer TLS credentials: %w", err)
		}
		options = append(options, grpc.WithTransportCredentials(creds))
	} else {
		options = append(options, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.DialContext(dialCtx, dialAddr, options...)
	if err != nil {
		return nil, fmt.Errorf("dial orderer %s at %s: %w", orderer.ID(), dialAddr, err)
	}
	return conn, nil
}
