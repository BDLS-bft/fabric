/*
Copyright IBM Corp All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"encoding/json"
	"testing"

	"github.com/hyperledger/fabric/integration"
	"github.com/hyperledger/fabric/integration/nwo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBDLS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "BDLS-based Ordering Service Suite")
}

var (
	buildServer *nwo.BuildServer
	components  *nwo.Components
)

var _ = SynchronizedBeforeSuite(func() []byte {
	buildServer = nwo.NewBuildServer()
	if err := buildServer.Serve(); err != nil {
		components = &nwo.Components{}
	} else {
		components = buildServer.Components()
	}

	payload, err := json.Marshal(components)
	Expect(err).NotTo(HaveOccurred())

	return payload
}, func(payload []byte) {
	err := json.Unmarshal(payload, &components)
	Expect(err).NotTo(HaveOccurred())
})

var _ = SynchronizedAfterSuite(func() {
}, func() {
	if buildServer != nil {
		buildServer.Shutdown()
	}
})

func StartPort() int {
	return integration.BDLSBasePort.StartPortForNode()
}
