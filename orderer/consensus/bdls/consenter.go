package bdls

import "fmt"

type Consenter struct {
	Logger   string
	Identity string
	Config   string
}

// New creates a new BDLS Consenter
func New(logger, identity, config string) *Consenter {
	fmt.Printf("Logger: %s\n", logger)
	fmt.Printf("Identity: %s\n", identity)
	fmt.Printf("Config: %s\n", config)

	return &Consenter{

		Logger:   logger,
		Identity: identity,
		Config:   config,
	}

}
