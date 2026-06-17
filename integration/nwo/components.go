/*
Copyright IBM Corp All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package nwo

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/hyperledger/fabric/integration/nwo/runner"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

type Components struct {
	ServerAddress string `json:"server_address"`
	cache         map[string]string
	cacheMutex    sync.Mutex
}

func (c *Components) ConfigTxGen() string {
	return c.Build("github.com/hyperledger/fabric/cmd/configtxgen")
}

func (c *Components) Cryptogen() string {
	return c.Build("github.com/hyperledger/fabric/cmd/cryptogen")
}

func (c *Components) Discover() string {
	return c.Build("github.com/hyperledger/fabric/cmd/discover")
}

func (c *Components) Idemixgen() string {
	if c.ServerAddress == "" {
		return c.buildAndCache("github.com/IBM/idemix/tools/idemixgen", "-mod=mod")
	}

	idemixgen, err := gexec.Build("github.com/IBM/idemix/tools/idemixgen", "-mod=mod")
	Expect(err).NotTo(HaveOccurred())
	return idemixgen
}

func (c *Components) Orderer() string {
	return c.Build("github.com/hyperledger/fabric/cmd/orderer")
}

func (c *Components) Osnadmin() string {
	return c.Build("github.com/hyperledger/fabric/cmd/osnadmin")
}

func (c *Components) Peer() string {
	return c.Build("github.com/hyperledger/fabric/cmd/peer")
}

func (c *Components) Cleanup() {}

func (c *Components) Build(path string) string {
	if c.ServerAddress == "" {
		return c.buildAndCache(path)
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/%s", c.ServerAddress, path))
	Expect(err).NotTo(HaveOccurred())

	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())

	if resp.StatusCode != http.StatusOK {
		Expect(resp.StatusCode).To(Equal(http.StatusOK), string(body))
	}

	return string(body)
}

func (c *Components) buildAndCache(path string, buildArgs ...string) string {
	c.cacheMutex.Lock()
	defer c.cacheMutex.Unlock()

	if c.cache == nil {
		c.cache = map[string]string{}
	}

	cacheKey := path
	if len(buildArgs) > 0 {
		cacheKey = cacheKey + ":" + strings.Join(buildArgs, "\x00")
	}

	if bin, ok := c.cache[cacheKey]; ok {
		return bin
	}

	output, err := gexec.Build(path, buildArgs...)
	Expect(err).NotTo(HaveOccurred())
	c.cache[cacheKey] = output
	return output
}

const CCEnvDefaultImage = "hyperledger/fabric-ccenv:latest"

var RequiredImages = []string{
	CCEnvDefaultImage,
	runner.CouchDBDefaultImage,
}
