//go:build unix

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// detachedProcAttr starts ssh in its own session so it isn't killed by the
// terminal's SIGHUP when lport detaches and the terminal is closed
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// processAlive reports whether a process with the given pid exists
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// findExistingForwards lists running ssh forward processes spawned by lport
func findExistingForwards() []ExistingForward {
	out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "command=").Output()
	if err != nil {
		return nil
	}

	var forwards []ExistingForward
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == os.Getpid() {
			continue
		}
		host, localPort, remotePort, ok := parseForwardCommand(fields[1:])
		if !ok {
			continue
		}
		forwards = append(forwards, ExistingForward{
			PID:        pid,
			Host:       host,
			LocalPort:  localPort,
			RemotePort: remotePort,
		})
	}
	return forwards
}
