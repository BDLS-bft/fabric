package bdls

import (
	"bytes"
	"fmt"

	"github.com/IBM/idemix/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/orderer/common/cluster"
)

type Consenter struct {
	Comm     *cluster.AuthCommMgr
	Logger   *flogging.FabricLogger
	Identity string
	Config   string
}

// New creates a new BDLS Consenter
func New(logger, identity, config string) *Consenter {
	fmt.Printf("Logger: %s\n", logger)
	fmt.Printf("Identity: %s\n", identity)
	fmt.Printf("Config: %s\n", config)

	return &Consenter{

		Identity: identity,
		Config:   config,
	}

}

func (c *Consenter) detectSelfID(consenters []*common.Consenter) (uint32, error) {
	for _, cst := range consenters {
		santizedCert, err := crypto.SanitizeX509Cert(cst.Identity)
		if err != nil {
			return 0, err
		}
		if bytes.Equal(c.Comm.NodeIdentity, santizedCert) {
			return cst.Id, nil
		}
	}
	c.Logger.Warning("Could not find the node in channel consenters set")
	return 0, cluster.ErrNotInChannel
}
