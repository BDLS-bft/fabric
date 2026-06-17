/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package nwo

import (
	"os"
	"os/exec"
	"strings"
)

type Command interface {
	Args() []string
	SessionName() string
}

type Enver interface {
	Env() []string
}

type WorkingDirer interface {
	WorkingDir() string
}

func cleanEnv(env []string) []string {
	var clean []string
	for _, envVar := range env {
		if strings.HasPrefix(envVar, "CORE_PEER_") || strings.HasPrefix(envVar, "CORE_ORDERER_") {
			continue
		}
		clean = append(clean, envVar)
	}
	return clean
}

func NewCommand(path string, command Command) *exec.Cmd {
	cmd := exec.Command(path, command.Args()...)
	cmd.Env = cleanEnv(os.Environ())
	if ce, ok := command.(Enver); ok {
		cmd.Env = append(cmd.Env, ce.Env()...)
	}
	if wd, ok := command.(WorkingDirer); ok {
		cmd.Dir = wd.WorkingDir()
	}
	return cmd
}
