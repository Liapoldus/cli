//go:build !windows

package localcore

import (
	"errors"
	"os"
	"syscall"
)

func terminateProcess(process *os.Process, _ int) error {
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func processAlive(process *os.Process, _ int) bool {
	return process.Signal(syscall.Signal(0)) == nil
}
