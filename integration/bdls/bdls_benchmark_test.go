/*
Copyright IBM Corp All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/integration"
	"github.com/hyperledger/fabric/integration/nwo"
	"github.com/hyperledger/fabric/integration/nwo/commands"
	dcli "github.com/moby/moby/client"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"google.golang.org/protobuf/proto"
)

var (
	benchConsensus           = flag.String("bdls.bench.consensus", "BDLS", "consensus to benchmark: BDLS, BFT, etcdraft, or all")
	benchBatchTimeout        = flag.Duration("bdls.bench.batch-timeout", time.Second, "Fabric orderer BatchTimeout used by the benchmark channel")
	benchMaxMessageCount     = flag.Int("bdls.bench.max-message-count", 500, "Fabric orderer BatchSize.MaxMessageCount used by the benchmark channel")
	benchAbsoluteMaxBytesMB  = flag.Int("bdls.bench.absolute-max-bytes-mb", 10, "Fabric orderer BatchSize.AbsoluteMaxBytes in MB")
	benchPreferredMaxBytesKB = flag.Int("bdls.bench.preferred-max-bytes-kb", 512, "Fabric orderer BatchSize.PreferredMaxBytes in KB")
	benchPayloadBytes        = flag.Int("bdls.bench.payload-bytes", 0, "response payload bytes for the simple chaincode respond path; 0 uses state-changing invoke")
	benchBDLSDelta0          = flag.Duration("bdls.bench.delta0", 0, "BDLS delta0 timeout; 0 uses the BDLS library default")
	benchBDLSDelta1          = flag.Duration("bdls.bench.delta1", 0, "BDLS delta1 timeout; 0 uses the BDLS library default")
	benchBDLSDeltaPrime1     = flag.Duration("bdls.bench.delta-prime1", 0, "BDLS delta-prime1 timeout; 0 uses the BDLS library default")
	benchBDLSDelta2          = flag.Duration("bdls.bench.delta2", 0, "BDLS delta2 timeout; 0 uses the BDLS library default")
	benchBDLSDelta3          = flag.Duration("bdls.bench.delta3", 0, "BDLS delta3 timeout; 0 uses the BDLS library default")
	benchBDLSLatency         = flag.Duration("bdls.bench.latency", 0, "BDLS base latency; 0 uses the BDLS library default")
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
	defer stopBenchmarkProcesses(b, ordererProcesses)

	peerProcesses := startBenchmarkPeers(b, network)
	defer stopBenchmarkProcess(b, peerProcesses, network.EventuallyTimeout)

	joinBenchmarkChannel(b, network, channel)
	time.Sleep(5 * time.Second)
	network.JoinChannel(channel, network.Orderers[0], network.PeersWithChannel(channel)...)
	deployBenchmarkChaincode(network, channel, testDir, components)

	peer := network.Peer("Org1", "peer0")
	for _, metric := range []struct {
		name  string
		value float64
	}{
		{"batch_timeout_ms", float64(benchBatchTimeout.Milliseconds())},
		{"max_message_count", float64(*benchMaxMessageCount)},
		{"absolute_max_bytes_mb", float64(*benchAbsoluteMaxBytesMB)},
		{"preferred_max_bytes_kb", float64(*benchPreferredMaxBytesKB)},
		{"payload_bytes", float64(*benchPayloadBytes)},
		{"bdls_delta0_ms", float64(benchBDLSDelta0.Milliseconds())},
		{"bdls_delta1_ms", float64(benchBDLSDelta1.Milliseconds())},
		{"bdls_delta_prime1_ms", float64(benchBDLSDeltaPrime1.Milliseconds())},
		{"bdls_delta2_ms", float64(benchBDLSDelta2.Milliseconds())},
		{"bdls_delta3_ms", float64(benchBDLSDelta3.Milliseconds())},
		{"bdls_latency_ms", float64(benchBDLSLatency.Milliseconds())},
	} {
		b.ReportMetric(metric.value, metric.name)
	}

	b.ResetTimer()
	started := time.Now()
	for i := 0; i < b.N; i++ {
		benchmarkInvoke(b, network, peer, network.Orderers[i%len(network.Orderers)], channel)
	}
	elapsed := time.Since(started)
	b.StopTimer()

	if elapsed > 0 {
		b.ReportMetric(float64(b.N)/elapsed.Seconds(), "tx/s")
	}
}

func benchmarkComponents(b *testing.B) (*nwo.Components, func()) {
	b.Helper()
	if components != nil {
		return components, func() {}
	}

	server := nwo.NewBuildServer()
	server.Serve()
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
				ReliableDecide:            true,
			}
		}
	}
	for _, peer := range config.Peers {
		peer.Channels = []*nwo.PeerChannel{{Name: channel, Anchor: true}}
	}
	return config
}

func startBenchmarkOrderers(b *testing.B, network *nwo.Network) []ifrit.Process {
	b.Helper()
	var processes []ifrit.Process
	for _, orderer := range network.Orderers {
		runner := network.OrdererRunner(orderer)
		runner.Command.Env = append(runner.Command.Env, "FABRIC_LOGGING_SPEC=orderer.consensus.bdls=info:orderer.consensus.smartbft=info:orderer.consensus.etcdraft=info")
		proc := ifrit.Invoke(runner)
		processes = append(processes, proc)
		gomega.Eventually(proc.Ready(), network.EventuallyTimeout).Should(gomega.BeClosed())
	}
	return processes
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

func stopBenchmarkProcesses(b *testing.B, processes []ifrit.Process) {
	b.Helper()
	for _, proc := range processes {
		if proc == nil {
			continue
		}
		proc.Signal(syscall.SIGTERM)
		gomega.Eventually(proc.Wait(), time.Minute).Should(gomega.Receive())
	}
}

func stopBenchmarkProcess(b *testing.B, process ifrit.Process, timeout time.Duration) {
	b.Helper()
	if process == nil {
		return
	}
	process.Signal(syscall.SIGTERM)
	gomega.Eventually(process.Wait(), timeout).Should(gomega.Receive())
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
		URL:               "/participation/v1/channels/" + channel,
		Status:            "active",
		ConsensusRelation: "consenter",
		Height:            1,
	}

	for _, orderer := range network.Orderers {
		nwo.Join(network, orderer, channel, genesisBlock, expectedChannelInfoPT)
		channelInfo := nwo.ListOne(network, orderer, channel)
		gomega.Expect(channelInfo).To(gomega.Equal(expectedChannelInfoPT))
	}
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

func benchmarkInvoke(b *testing.B, network *nwo.Network, peer *nwo.Peer, orderer *nwo.Orderer, channel string) {
	b.Helper()
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
		b.Fatalf("create invoke session: %v", err)
	}
	gomega.Eventually(sess, 2*time.Minute).Should(gexec.Exit(0))
	gomega.Expect(sess.Err).To(gbytes.Say("Chaincode invoke successful. result: status:200"))
}

func benchmarkCtor(payloadBytes int) string {
	if payloadBytes <= 0 {
		return `{"Args":["invoke","a","b","1"]}`
	}
	return fmt.Sprintf(`{"Args":["respond","200","ok","%s"]}`, strings.Repeat("x", payloadBytes))
}
