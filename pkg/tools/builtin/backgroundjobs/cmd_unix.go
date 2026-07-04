//go:build !windows && !js

package backgroundjobs

import (
	"os"
	"syscall"
)

type processGroup struct{}

func platformSpecificSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func createProcessGroup(_ *os.Process) (*processGroup, error) {
	return &processGroup{}, nil
}

func kill(proc *os.Process, _ *processGroup) error {
	return syscall.Kill(-proc.Pid, syscall.SIGTERM)
}
