//go:build windows

package localcore

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func terminateProcess(_ *os.Process, pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

func processAlive(_ *os.Process, pid int) bool {
	output, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	return err == nil && bytes.Contains(output, []byte(fmt.Sprintf("\"%d\"", pid)))
}
