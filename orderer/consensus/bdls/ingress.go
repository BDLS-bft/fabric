/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	//protos "github.com/SmartBFT-Go/consensus/smartbftprotos"
	"github.com/BDLS-bft/bdls"
	ab "github.com/hyperledger/fabric-protos-go/orderer"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	googleproto "google.golang.org/protobuf/proto"
)

//go:generate mockery -dir . -name MessageReceiver -case underscore -output mocks

// MessageReceiver receives messages
type MessageReceiver interface {
	HandleMessage(sender uint64, m []byte)
	HandleRequest(sender uint64, req []byte)
}

//go:generate mockery -dir . -name ReceiverGetter -case underscore -output mocks

// ReceiverGetter obtains instances of MessageReceiver given a channel ID
type ReceiverGetter interface {
	// ReceiverByChain returns the MessageReceiver if it exists, or nil if it doesn't
	ReceiverByChain(channelID string) MessageReceiver
}

type WarningLogger interface {
	Warningf(template string, args ...interface{})
	Debugf(template string, args ...interface{})
}

// Ingress dispatches Submit and Step requests to the designated per chain instances
type Ingress struct {
	Logger        WarningLogger
	ChainSelector ReceiverGetter
}

// OnConsensus notifies the Ingress for a reception of a StepRequest from a given sender on a given channel
func (in *Ingress) OnConsensus(channel string, sender uint64, request *ab.ConsensusRequest) error {
	if request == nil {
		in.Logger.Warningf("Received nil consensus request from %d on channel %s", sender, channel)
		return errors.Errorf("nil consensus request")
	}

	receiver := in.ChainSelector.ReceiverByChain(channel)
	if receiver == nil {
		in.Logger.Warningf("An attempt to send a consensus request to a non existing channel (%s) was made by %d", channel, sender)
		return errors.Errorf("channel %s doesn't exist", channel)
	}

	payload := request.Payload
	if len(payload) == 0 {
		in.Logger.Debugf("Consensus request from %d on channel %s has empty payload", sender, channel)
		receiver.HandleMessage(sender, payload)
		return nil
	}

	in.Logger.Debugf("Consensus payload from %d on channel %s: len=%d bytes", sender, channel, len(payload))

	signed := &bdls.SignedProto{}
	if err := googleproto.Unmarshal(payload, signed); err != nil {
		in.Logger.Warningf("Failed to decode BDLS signed payload from %d on channel %s: %v", sender, channel, err)
		return errors.Wrap(err, "malformed BDLS consensus payload")
	}

	msg := &bdls.Message{}
	if err := googleproto.Unmarshal(signed.Message, msg); err != nil {
		in.Logger.Warningf("Failed to decode BDLS message body from %d on channel %s: %v", sender, channel, err)
	} else {
		in.Logger.Debugf("Consensus message from %d on channel %s: type=%s height=%d round=%d", sender, channel, msg.Type.String(), msg.Height, msg.Round)
	}

	receiver.HandleMessage(sender, payload)
	return nil
}

// OnSubmit notifies the Ingress for a reception of a SubmitRequest from a given sender on a given channel
func (in *Ingress) OnSubmit(channel string, sender uint64, request *ab.SubmitRequest) error {
	receiver := in.ChainSelector.ReceiverByChain(channel)
	if receiver == nil {
		in.Logger.Warningf("An attempt to submit a transaction to a non existing channel (%s) was made by %d", channel, sender)
		return errors.Errorf("channel %s doesn't exist", channel)
	}
	receiver.HandleRequest(sender, protoutil.MarshalOrPanic(request.Payload))
	return nil
}
