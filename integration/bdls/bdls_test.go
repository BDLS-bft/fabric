/*
Copyright IBM Corp All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hyperledger/fabric/integration/nwo"
	"github.com/hyperledger/fabric/integration/nwo/commands"
	dcli "github.com/moby/moby/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/pkg/errors"
	"github.com/tedsuo/ifrit"
	ginkgomon "github.com/tedsuo/ifrit/ginkgomon_v2"
	"github.com/tedsuo/ifrit/grouper"
)

const ordererAdminRequestTimeout = 5 * time.Second

var _ = Describe("EndToEnd BDLS ordering service", func() {
	var (
		testDir          string
		client           dcli.APIClient
		network          *nwo.Network
		ordererProcesses []ifrit.Process
		peerProcesses    ifrit.Process
	)

	BeforeEach(func() {
		ordererProcesses = nil
		peerProcesses = nil
		var err error
		testDir, err = os.MkdirTemp("", "e2e-bdls-test")
		Expect(err).NotTo(HaveOccurred())

		client, err = dcli.New(dcli.FromEnv)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if peerProcesses != nil {
			stopProcess(peerProcesses, network.EventuallyTimeout)
		}
		for _, proc := range ordererProcesses {
			stopProcess(proc, network.EventuallyTimeout)
		}
		if network != nil {
			network.Cleanup()
		}
		os.RemoveAll(testDir)
	})

	Describe("BDLS 4-node network", func() {
		It("orders transactions and survives stop-start of all nodes", func() {
			networkConfig := nwo.MultiNodeBDLS()
			networkConfig.Channels = nil
			channel := "testchannel1"

			network = nwo.New(networkConfig, testDir, client, StartPort(), components)
			network.GenerateConfigTree()
			network.Bootstrap()

			for _, orderer := range network.Orderers {
				runner := network.OrdererRunner(orderer)
				runner.Command.Env = append(runner.Command.Env, "FABRIC_LOGGING_SPEC=orderer.consensus.bdls=debug:orderer.common.multichannel=debug:orderer.common.broadcast=debug:deliveryClient=debug:grpc=debug")
				proc := ifrit.Invoke(runner)
				ordererProcesses = append(ordererProcesses, proc)
				Eventually(proc.Ready(), network.EventuallyTimeout).Should(BeClosed())
			}

			peerGroupRunner, _ := peerGroupRunners(network)
			peerProcesses = ifrit.Invoke(peerGroupRunner)
			Eventually(peerProcesses.Ready(), network.EventuallyTimeout).Should(BeClosed())
			peer := network.Peer("Org1", "peer0")

			By("Joining orderers to channel")
			joinChannel(network, channel)

			By("Joining peers to channel")
			network.JoinChannel(channel, network.Orderers[0], network.PeersWithChannel(channel)...)

			By("Deploying chaincode")
			deployChaincode(network, channel, testDir)

			By("Querying the chaincode (initial)")
			sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeQuery{
				ChannelID: channel,
				Name:      "mycc",
				Ctor:      `{"Args":["query","a"]}`,
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			Expect(sess).To(gbytes.Say("100"))

			By("Invoking the chaincode")
			invokeQuery(network, peer, network.Orderers[1], channel, 90)

			By("Taking down all orderers")
			for _, proc := range ordererProcesses {
				stopProcess(proc, network.EventuallyTimeout)
			}

			ordererProcesses = nil

			By("Bringing all orderers back up")
			for _, orderer := range network.Orderers {
				runner := network.OrdererRunner(orderer)
				runner.Command.Env = append(runner.Command.Env, "FABRIC_LOGGING_SPEC=orderer.consensus.bdls=debug:orderer.common.multichannel=debug:orderer.common.broadcast=debug:deliveryClient=debug:grpc=debug")
				proc := ifrit.Invoke(runner)
				ordererProcesses = append(ordererProcesses, proc)
				Eventually(proc.Ready(), network.EventuallyTimeout).Should(BeClosed())
			}

			By("Waiting for BDLS to resume consensus")
			waitForBDLSChannelActive(network, network.Orderers, "testchannel1")

			By("Invoking the chaincode again after restart")
			invokeQuery(network, peer, network.Orderers[2], channel, 80)
		})

		It("maintains liveness when one node is down (f=1)", func() {
			networkConfig := nwo.MultiNodeBDLS()
			networkConfig.Channels = nil
			channel := "testchannel1"

			network = nwo.New(networkConfig, testDir, client, StartPort(), components)
			network.GenerateConfigTree()
			network.Bootstrap()

			for _, orderer := range network.Orderers {
				runner := network.OrdererRunner(orderer)
				runner.Command.Env = append(runner.Command.Env, "FABRIC_LOGGING_SPEC=orderer.consensus.bdls=debug:orderer.common.multichannel=debug:orderer.common.broadcast=debug:deliveryClient=debug:grpc=debug")
				proc := ifrit.Invoke(runner)
				ordererProcesses = append(ordererProcesses, proc)
				Eventually(proc.Ready(), network.EventuallyTimeout).Should(BeClosed())
			}

			peerGroupRunner, _ := peerGroupRunners(network)
			peerProcesses = ifrit.Invoke(peerGroupRunner)
			Eventually(peerProcesses.Ready(), network.EventuallyTimeout).Should(BeClosed())
			peer := network.Peer("Org1", "peer0")

			joinChannel(network, channel)
			network.JoinChannel(channel, network.Orderers[0], network.PeersWithChannel(channel)...)
			deployChaincode(network, channel, testDir)

			By("Killing one orderer (1 of 4 = within f=1 tolerance)")
			if len(ordererProcesses) < 4 {
				Fail(fmt.Sprintf("expected at least 4 orderers for f=1 failure scenario, got %d", len(ordererProcesses)))
			}
			killProcess(ordererProcesses[3], network.EventuallyTimeout)

			By("Invoking chaincode — should still succeed with 3/4 orderers")
			invokeQuery(network, peer, network.Orderers[0], channel, 90)
			invokeQuery(network, peer, network.Orderers[1], channel, 80)
		})
	})
})

// ---------------------------------------------------------------------------
// Helpers — mirrors of the smartbft integration test helpers
// ---------------------------------------------------------------------------

func joinChannel(network *nwo.Network, channel string) {
	genesisBlockBytes, err := os.ReadFile(network.OutputBlockPath(channel))
	if err != nil {
		if !errors.Is(err, syscall.ENOENT) {
			Expect(err).NotTo(HaveOccurred())
		}
		sess, err := network.ConfigTxGen(commands.OutputBlock{
			ChannelID:   channel,
			Profile:     network.Profiles[0].Name,
			ConfigPath:  network.RootDir,
			OutputBlock: network.OutputBlockPath(channel),
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))

		genesisBlockBytes, err = os.ReadFile(network.OutputBlockPath(channel))
		Expect(err).NotTo(HaveOccurred())
	}

	for _, o := range network.Orderers {
		By("joining " + o.Name + " to channel as a consenter")
		joinOrdererWithRetry(network, o, channel, genesisBlockBytes)
	}
	waitForBDLSChannelActive(network, network.Orderers, channel)
}

func joinOrdererWithRetry(network *nwo.Network, orderer *nwo.Orderer, channel string, genesisBlockBytes []byte) {
	By(fmt.Sprintf("retrying join for %s to channel %s", orderer.ID(), channel))
	Eventually(func() error {
		return joinOrderer(network, orderer, channel, genesisBlockBytes)
	}, network.EventuallyTimeout, 250*time.Millisecond).Should(Succeed())
}

func joinOrderer(network *nwo.Network, orderer *nwo.Orderer, channel string, genesisBlockBytes []byte) error {
	protocol := "http"
	if network.TLSEnabled {
		protocol = "https"
	}
	url := fmt.Sprintf("%s://127.0.0.1:%d/participation/v1/channels", protocol, network.OrdererPort(orderer, nwo.AdminPort))
	req := nwo.GenerateJoinRequest(url, channel, genesisBlockBytes)
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
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	bodyText := strings.TrimSpace(string(body))
	alreadyJoined := (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict) &&
		(strings.Contains(strings.ToLower(bodyText), "already") || strings.Contains(strings.ToLower(bodyText), "exists"))
	if resp.StatusCode != http.StatusCreated && !alreadyJoined {
		return fmt.Errorf("join request failed with status %d: %s", resp.StatusCode, bodyText)
	}
	if alreadyJoined {
		return nil
	}
	if len(bodyText) == 0 {
		return nil
	}

	channelInfo := nwo.ChannelInfo{}
	if err := json.Unmarshal(body, &channelInfo); err != nil {
		return err
	}
	if channelInfo.Name != channel {
		return fmt.Errorf("joined channel name mismatch: got %q want %q", channelInfo.Name, channel)
	}
	return nil
}

func waitForBDLSChannelActive(network *nwo.Network, orderers []*nwo.Orderer, channel string) {
	By("waiting for BDLS channel to become active")
	if len(orderers) == 0 {
		Fail("no orderers configured")
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
			Fail(err.Error())
		}
	}
}

func peerGroupRunners(n *nwo.Network) (ifrit.Runner, []*ginkgomon.Runner) {
	runners := []*ginkgomon.Runner{}
	members := grouper.Members{}
	for _, p := range n.Peers {
		runner := n.PeerRunner(p)
		runner.Command.Env = append(runner.Command.Env, "FABRIC_LOGGING_SPEC=debug")
		members = append(members, grouper.Member{Name: p.ID(), Runner: runner})
		runners = append(runners, runner)
	}
	return grouper.NewParallel(syscall.SIGTERM, members), runners
}

func stopProcess(process ifrit.Process, timeout time.Duration) {
	if process == nil {
		return
	}

	select {
	case <-process.Wait():
		return
	default:
	}

	process.Signal(syscall.SIGTERM)

	select {
	case <-process.Wait():
		return
	case <-time.After(timeout):
	}

	select {
	case <-process.Wait():
		return
	default:
	}
	process.Signal(syscall.SIGKILL)
	select {
	case <-process.Wait():
	case <-time.After(timeout):
	}
}

func killProcess(process ifrit.Process, timeout time.Duration) {
	if process == nil {
		return
	}
	select {
	case <-process.Wait():
		return
	default:
	}

	process.Signal(syscall.SIGKILL)
	select {
	case <-process.Wait():
	case <-time.After(timeout):
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func deployChaincode(network *nwo.Network, channel string, testDir string) {
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

func invokeQuery(network *nwo.Network, peer *nwo.Peer, orderer *nwo.Orderer, channel string, expectedBalance int) {
	sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeInvoke{
		ChannelID: channel,
		Orderer:   network.OrdererAddress(orderer, nwo.ListenPort),
		Name:      "mycc",
		Ctor:      `{"Args":["invoke","a","b","10"]}`,
		PeerAddresses: []string{
			network.PeerAddress(network.Peer("Org1", "peer0"), nwo.ListenPort),
			network.PeerAddress(network.Peer("Org2", "peer0"), nwo.ListenPort),
		},
		WaitForEvent: true,
	})
	Expect(err).NotTo(HaveOccurred())
	Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
	Expect(sess.Err).To(gbytes.Say("Chaincode invoke successful. result: status:200"))

	Eventually(func() string {
		sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeQuery{
			ChannelID: channel,
			Name:      "mycc",
			Ctor:      `{"Args":["query","a"]}`,
		})
		Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
		if sess.ExitCode() != 0 {
			return fmt.Sprintf("exit code is %d: %s, %v", sess.ExitCode(), string(sess.Err.Contents()), err)
		}
		return string(sess.Out.Contents())
	}, network.EventuallyTimeout, time.Second).Should(ContainSubstring(fmt.Sprintf("%d", expectedBalance)))
}

func waitForOrdererChannelActive(orderer *nwo.Orderer, network *nwo.Network, channel string, timeout time.Duration) error {
	requestTimeout := timeout / 10
	if requestTimeout < time.Second {
		requestTimeout = time.Second
	}
	if requestTimeout > 5*time.Second {
		requestTimeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		channelInfo, err := listOrdererChannelInfo(network, orderer, channel, minDuration(requestTimeout, remaining))
		if err == nil {
			if channelInfo.Name != "" && channelInfo.Name != channel {
				lastErr = fmt.Errorf("orderer %s channel endpoint %s returned info for different channel %q", orderer.ID(), channel, channelInfo.Name)
			} else if channelInfo.Status == "active" &&
				channelInfo.ConsensusRelation == "consenter" &&
				channelInfo.Height >= 1 {
				return nil
			} else {
				lastErr = fmt.Errorf("orderer %s channel %s not active: status=%q relation=%q height=%d",
					orderer.ID(), channel, channelInfo.Status, channelInfo.ConsensusRelation, channelInfo.Height)
			}
		} else {
			lastErr = err
		}
		if sleep := minDuration(time.Second, time.Until(deadline)); sleep > 0 {
			time.Sleep(sleep)
		}
	}
	return fmt.Errorf("timed out waiting for orderer %s channel %s to become active: %w", orderer.ID(), channel, lastErr)
}

func listOrdererChannelInfo(network *nwo.Network, orderer *nwo.Orderer, channel string, requestTimeout time.Duration) (nwo.ChannelInfo, error) {
	protocol := "http"
	if network.TLSEnabled {
		protocol = "https"
	}
	listChannelURL := fmt.Sprintf(
		"%s://127.0.0.1:%d/participation/v1/channels/%s",
		protocol,
		network.OrdererPort(orderer, nwo.AdminPort),
		channel,
	)

	authClient, unauthClient := nwo.OrdererOperationalClients(network, orderer)
	client := unauthClient
	if network.TLSEnabled {
		client = authClient
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, listChannelURL, nil)
	if err != nil {
		return nwo.ChannelInfo{}, fmt.Errorf("build participation channel info request for orderer %s: %w", orderer.ID(), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := client.Do(req)
	if err != nil {
		return nwo.ChannelInfo{}, fmt.Errorf("get participation channel info for orderer %s: %w", orderer.ID(), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nwo.ChannelInfo{}, fmt.Errorf("read participation channel info for orderer %s: %w", orderer.ID(), err)
	}
	if resp.StatusCode != http.StatusOK {
		return nwo.ChannelInfo{}, fmt.Errorf("participation channel info for orderer %s returned status %d: %s", orderer.ID(), resp.StatusCode, strings.TrimSpace(string(body)))
	}

	channelInfo := nwo.ChannelInfo{}
	if err := json.Unmarshal(body, &channelInfo); err != nil {
		return nwo.ChannelInfo{}, fmt.Errorf("unmarshal participation channel info for orderer %s: %w", orderer.ID(), err)
	}
	return channelInfo, nil
}
