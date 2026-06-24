/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Package bdls implements a Fabric orderer consenter backed by the BDLS
// consensus protocol (https://eprint.iacr.org/2019/1460). It plugs into the
// existing orderer/common/cluster gRPC transport, so inter-orderer messages
// travel on the same ConsensusRequest/SubmitRequest streams that etcdraft and
// smartbft already use — BDLS does not open a second listener.
//
// This package is the Fabric side of the integration. The consensus state
// machine itself lives in github.com/BDLS-bft/bdls and is pinned via go.mod.
package bdls
