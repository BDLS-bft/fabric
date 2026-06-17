/*
Copyright IBM Corp All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
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
	"google.golang.org/protobuf/proto"
)

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
			peerProcesses.Signal(syscall.SIGTERM)
			Eventually(peerProcesses.Wait(), network.EventuallyTimeout).Should(Receive())
		}
		if network != nil {
			network.Cleanup()
		}
		for _, proc := range ordererProcesses {
			proc.Signal(syscall.SIGTERM)
			Eventually(proc.Wait(), network.EventuallyTimeout).Should(Receive())
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

			By("Waiting for BDLS consensus to initialise")
			// Give the BDLS state machine time to start its first height.
			time.Sleep(5 * time.Second)

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
				proc.Signal(syscall.SIGTERM)
				Eventually(proc.Wait(), network.EventuallyTimeout).Should(Receive())
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
			time.Sleep(5 * time.Second)

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
			time.Sleep(5 * time.Second)
			network.JoinChannel(channel, network.Orderers[0], network.PeersWithChannel(channel)...)
			deployChaincode(network, channel, testDir)

			By("Killing orderer4 (1 of 4 = within f=1 tolerance)")
			ordererProcesses[3].Signal(syscall.SIGKILL)
			Eventually(ordererProcesses[3].Wait(), network.EventuallyTimeout).Should(Receive())

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
	if err != nil && errors.Is(err, syscall.ENOENT) {
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

	genesisBlock := &common.Block{}
	err = proto.Unmarshal(genesisBlockBytes, genesisBlock)
	Expect(err).NotTo(HaveOccurred())

	expectedChannelInfoPT := nwo.ChannelInfo{
		Name:              channel,
		URL:               "/participation/v1/channels/" + channel,
		Status:            "active",
		ConsensusRelation: "consenter",
		Height:            1,
	}

	for _, o := range network.Orderers {
		By("joining " + o.Name + " to channel as a consenter")
		nwo.Join(network, o, channel, genesisBlock, expectedChannelInfoPT)
		channelInfo := nwo.ListOne(network, o, channel)
		Expect(channelInfo).To(Equal(expectedChannelInfoPT))
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
		Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit())
		if sess.ExitCode() != 0 {
			return fmt.Sprintf("exit code is %d: %s, %v", sess.ExitCode(), string(sess.Err.Contents()), err)
		}
		return string(sess.Out.Contents())
	}, network.EventuallyTimeout, time.Second).Should(ContainSubstring(fmt.Sprintf("%d", expectedBalance)))
}
