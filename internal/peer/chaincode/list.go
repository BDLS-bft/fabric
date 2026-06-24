/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package chaincode

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hyperledger/fabric-lib-go/bccsp"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	pb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

var (
	chaincodeListCmd          *cobra.Command
	getInstalledChaincodes    bool
	getInstantiatedChaincodes bool
)

// listCmd returns the cobra command for listing
// the installed or instantiated chaincodes
func listCmd(cf *ChaincodeCmdFactory, cryptoProvider bccsp.BCCSP) *cobra.Command {
	chaincodeListCmd = &cobra.Command{
		Use:   "list",
		Short: "Get the instantiated chaincodes on a channel or installed chaincodes on a peer.",
		Long:  "Get the instantiated chaincodes in the channel if specify channel, or get installed chaincodes on the peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getChaincodes(cmd, cf, cryptoProvider)
		},
	}

	flagList := []string{
		"channelID",
		"installed",
		"instantiated",
		"peerAddresses",
		"tlsRootCertFiles",
		"connectionProfile",
	}
	attachFlags(chaincodeListCmd, flagList)

	return chaincodeListCmd
}

func getChaincodes(cmd *cobra.Command, cf *ChaincodeCmdFactory, cryptoProvider bccsp.BCCSP) error {
	if getInstantiatedChaincodes && channelID == "" {
		return errors.New("The required parameter 'channelID' is empty. Rerun the command with -C flag")
	}
	// Parsing of the command line is done so silence cmd usage
	cmd.SilenceUsage = true

	var err error
	if cf == nil {
		cf, err = InitCmdFactory(cmd.Name(), true, false, cryptoProvider)
		if err != nil {
			return err
		}
	}

	creator, err := cf.Signer.Serialize()
	if err != nil {
		return fmt.Errorf("error serializing identity: %s", err)
	}

	var proposal *pb.Proposal

	if getInstalledChaincodes == getInstantiatedChaincodes {
		return errors.New("must explicitly specify \"--installed\" or \"--instantiated\"")
	}

	if getInstalledChaincodes {
		proposal, _, err = protoutil.CreateGetInstalledChaincodesProposal(creator)
	}

	if getInstantiatedChaincodes {
		proposal, _, err = protoutil.CreateGetChaincodesProposal(channelID, creator)
	}

	if err != nil {
		return errors.WithMessage(err, "error creating proposal")
	}

	var signedProposal *pb.SignedProposal
	signedProposal, err = protoutil.GetSignedProposal(proposal, cf.Signer)
	if err != nil {
		return errors.WithMessage(err, "error creating signed proposal")
	}

	// list is currently only supported for one peer
	proposalResponse, err := cf.EndorserClients[0].ProcessProposal(context.Background(), signedProposal)
	if err != nil {
		return errors.WithMessage(err, "error endorsing proposal")
	}

	if proposalResponse.Response == nil {
		return errors.Errorf("proposal response had nil response")
	}

	if proposalResponse.Response.Status != int32(cb.Status_SUCCESS) {
		return errors.Errorf("bad response: %d - %s", proposalResponse.Response.Status, proposalResponse.Response.Message)
	}

	return printResponse(getInstalledChaincodes, getInstantiatedChaincodes, proposalResponse)
}

// printResponse prints the information included in the response
// from the server. If getInstalledChaincodes is set to false, the
// proposal response will be interpreted as containing instantiated
// chaincode information.
func printResponse(getInstalledChaincodes, getInstantiatedChaincodes bool, proposalResponse *pb.ProposalResponse) error {
	cqr := &pb.ChaincodeQueryResponse{}
	err := proto.Unmarshal(proposalResponse.Response.Payload, cqr)
	if err != nil {
		return err
	}

	if getInstalledChaincodes {
		fmt.Println("Get installed chaincodes on peer:")
	} else {
		fmt.Printf("Get instantiated chaincodes on channel %s:\n", channelID)
	}

	for _, chaincode := range cqr.Chaincodes {
		fmt.Printf("%v\n", ccInfo{chaincode}.String())
	}

	return nil
}

type ccInfo struct {
	*pb.ChaincodeInfo
}

func (cci ccInfo) String() string {
	var parts []string
	if cci.Name != "" {
		parts = append(parts, fmt.Sprintf("Name: %s", cci.Name))
	}
	if cci.Version != "" {
		parts = append(parts, fmt.Sprintf("Version: %s", cci.Version))
	}
	if cci.Path != "" {
		parts = append(parts, fmt.Sprintf("Path: %s", cci.Path))
	}
	if cci.Input != "" {
		parts = append(parts, fmt.Sprintf("Input: %s", cci.Input))
	}
	if cci.Escc != "" {
		parts = append(parts, fmt.Sprintf("Escc: %s", cci.Escc))
	}
	if cci.Vscc != "" {
		parts = append(parts, fmt.Sprintf("Vscc: %s", cci.Vscc))
	}
	if len(cci.Id) > 0 {
		parts = append(parts, fmt.Sprintf("Id: %s", hex.EncodeToString(cci.Id)))
	}
	return strings.Join(parts, ", ")
}
