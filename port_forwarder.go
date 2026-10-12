package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ForwardReadyMsg is sent when a forwarder is accepting connections on localhost
type ForwardReadyMsg struct {
	ID  int
	Key forwardKey
}

// ForwardFailedMsg is sent when a forwarder could not be established
type ForwardFailedMsg struct {
	ID    int
	Key   forwardKey
	Error error
}

// ForwardExitedMsg is sent when an established forwarder's ssh process exits
type ForwardExitedMsg struct {
	ID    int
	Key   forwardKey
	Error error
}

// forwardKey identifies a forward by SSH host alias and remote port
type forwardKey struct {
	host       string
	remotePort int
}

// PortForwarder manages a single SSH port forward using the ssh command.
// The ssh process runs in its own session so it can outlive lport (detach).
type PortForwarder struct {
	hostName   string
	localPort  int
	remotePort int
	process    *os.Process
	stderr     string
	done       chan struct{}
	exitErr    error
	stopOnce   sync.Once
}

// NewPortForwarder creates a new port forwarder using ssh command
func NewPortForwarder(hostName string, localPort, remotePort int) *PortForwarder {
	return &PortForwarder{
		hostName:   hostName,
		localPort:  localPort,
		remotePort: remotePort,
		done:       make(chan struct{}),
	}
}

// Start launches the ssh process. It returns once the process is spawned;
// use WaitReady to know when the tunnel actually accepts connections.
func (pf *PortForwarder) Start() error {
	// Format: ssh -L localport:localhost:remoteport hostname
	cmd := exec.Command("ssh",
		"-L", fmt.Sprintf("%d:localhost:%d", pf.localPort, pf.remotePort),
		"-N",                             // Don't execute remote command, just forward ports
		"-o", "ExitOnForwardFailure=yes", // Exit if port forwarding fails
		"-o", "BatchMode=yes", // Never prompt; prompts would corrupt the TUI
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30", // Keep connection alive
		"-o", "ServerAliveCountMax=3",
		pf.hostName)
	cmd.SysProcAttr = detachedProcAttr()

	// Capture stderr in a file so ssh messages don't draw over the TUI and can be
	// shown as errors. A pipe would SIGPIPE ssh once lport exits after a detach,
	// so use a file and unlink it right away; it disappears when both close it.
	logFile, err := os.CreateTemp("", "lport-ssh-*.log")
	if err != nil {
		return fmt.Errorf("failed to create ssh log: %w", err)
	}
	os.Remove(logFile.Name())
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start ssh: %w", err)
	}
	pf.process = cmd.Process

	go func() {
		pf.exitErr = cmd.Wait()
		stderr, _ := io.ReadAll(io.NewSectionReader(logFile, 0, 64*1024))
		pf.stderr = string(stderr)
		logFile.Close()
		close(pf.done)
	}()

	return nil
}

// AdoptPortForwarder takes over an ssh forward left running by a previous,
// detached lport session
func AdoptPortForwarder(hostName string, localPort, remotePort, pid int) *PortForwarder {
	pf := NewPortForwarder(hostName, localPort, remotePort)
	pf.process, _ = os.FindProcess(pid)

	// Not our child, so we can't Wait on it; poll instead
	go func() {
		for processAlive(pid) {
			time.Sleep(250 * time.Millisecond)
		}
		close(pf.done)
	}()

	return pf
}

// WaitReady blocks until the local port accepts connections, the ssh process
// exits, or the timeout elapses.
func (pf *PortForwarder) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", pf.localPort)

	for {
		select {
		case <-pf.done:
			return pf.exitError()
		default:
		}

		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for tunnel")
		}

		select {
		case <-pf.done:
			return pf.exitError()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Done returns a channel that is closed when the ssh process exits
func (pf *PortForwarder) Done() <-chan struct{} {
	return pf.done
}

// Stop kills the ssh process and waits for it to exit
func (pf *PortForwarder) Stop() {
	pf.stopOnce.Do(func() {
		if pf.process == nil {
			return
		}
		pf.process.Kill()
		<-pf.done
	})
}

// exitError describes why the ssh process exited. Only valid after done is closed.
func (pf *PortForwarder) exitError() error {
	lines := strings.Split(strings.TrimSpace(pf.stderr), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return fmt.Errorf("%s", last)
	}
	if pf.exitErr != nil {
		return fmt.Errorf("ssh exited: %w", pf.exitErr)
	}
	return fmt.Errorf("ssh exited")
}

// WaitForwardReady waits for a started forwarder to become usable
func WaitForwardReady(id int, key forwardKey, pf *PortForwarder) tea.Cmd {
	return func() tea.Msg {
		if err := pf.WaitReady(30 * time.Second); err != nil {
			pf.Stop()
			return ForwardFailedMsg{ID: id, Key: key, Error: err}
		}
		return ForwardReadyMsg{ID: id, Key: key}
	}
}

// WaitForwardExit reports when an established forwarder's ssh process exits
func WaitForwardExit(id int, key forwardKey, pf *PortForwarder) tea.Cmd {
	return func() tea.Msg {
		<-pf.Done()
		return ForwardExitedMsg{ID: id, Key: key, Error: pf.exitError()}
	}
}

// ExistingForwardsMsg is sent with forwards left running by a previous session
type ExistingForwardsMsg struct {
	Forwards []ExistingForward
}

// ExistingForward is a running ssh forward process spawned by lport (or kport)
type ExistingForward struct {
	PID        int
	Host       string
	LocalPort  int
	RemotePort int
}

// DiscoverForwards finds forwards left running by a previous, detached session
func DiscoverForwards() tea.Cmd {
	return func() tea.Msg {
		return ExistingForwardsMsg{Forwards: findExistingForwards()}
	}
}

// parseForwardCommand recognizes the ssh command line built by Start, e.g.
// "ssh -L 3000:localhost:3000 -N -o ExitOnForwardFailure=yes ... host".
// Requiring -N and ExitOnForwardFailure avoids picking up interactive ssh
// sessions the user started by hand.
func parseForwardCommand(args []string) (host string, localPort, remotePort int, ok bool) {
	if len(args) < 4 || filepath.Base(args[0]) != "ssh" {
		return "", 0, 0, false
	}
	hasN, hasExit := false, false
	for i, arg := range args {
		switch {
		case arg == "-N":
			hasN = true
		case arg == "ExitOnForwardFailure=yes":
			hasExit = true
		case arg == "-L" && i+1 < len(args):
			parts := strings.Split(args[i+1], ":")
			if len(parts) != 3 || parts[1] != "localhost" {
				return "", 0, 0, false
			}
			var err1, err2 error
			localPort, err1 = strconv.Atoi(parts[0])
			remotePort, err2 = strconv.Atoi(parts[2])
			if err1 != nil || err2 != nil {
				return "", 0, 0, false
			}
		}
	}
	if !hasN || !hasExit || localPort == 0 {
		return "", 0, 0, false
	}
	return args[len(args)-1], localPort, remotePort, true
}

// findPreferredLocalPort tries to use the same port as remote, fallback to random
func findPreferredLocalPort(remotePort int) (localPort int, samePort bool, err error) {
	// First try to use the same port as the remote port
	if isPortAvailable(remotePort) {
		return remotePort, true, nil
	}

	// If same port is not available, find any available port
	availablePort, err := findAvailablePort()
	if err != nil {
		return 0, false, err
	}

	return availablePort, false, nil
}

// isPortAvailable checks if a specific port is available locally. Loopback is
// checked explicitly since that's where ssh binds, and on some systems a
// wildcard bind succeeds even when loopback is taken.
func isPortAvailable(port int) bool {
	for _, addr := range []string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf(":%d", port)} {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		listener.Close()
	}
	return true
}

// findAvailablePort finds an available local port
func findAvailablePort() (int, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port, nil
}
