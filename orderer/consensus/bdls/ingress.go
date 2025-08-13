package bdls

import (
	ab "github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric/protoutil"
)

// ReceiverGetter obtains instances of BDLS Chain given a channel ID
type ReceiverGetter interface {
    ReceiverByChain(channelID string) *Chain
}

type WarningLogger interface {
    Warningf(template string, args ...interface{})
}

// Ingress dispatches Submit and Step requests to the designated per-chain instances
type Ingress struct {
    Logger        WarningLogger
    ChainSelector ReceiverGetter
}

func (in *Ingress) OnConsensus(channel string, sender uint64, request *ab.ConsensusRequest) error {
    receiver := in.ChainSelector.ReceiverByChain(channel)
    if receiver == nil {
        in.Logger.Warningf("An attempt to send a consensus request to a non existing channel (%s) was made by %d", channel, sender)
        return nil
    }
    receiver.ReceiveMessage(sender, request.Payload)
    return nil
}

func (in *Ingress) OnSubmit(channel string, sender uint64, request *ab.SubmitRequest) error {
    receiver := in.ChainSelector.ReceiverByChain(channel)
    if receiver == nil {
        in.Logger.Warningf("An attempt to submit a transaction to a non existing channel (%s) was made by %d", channel, sender)
        return nil
    }
    receiver.SubmitRequest(sender, protoutil.MarshalOrPanic(request.Payload))
    return nil
}
