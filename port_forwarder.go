package main

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ForwardReadyMsg is sent when a forwarder is accepting connections on localhost
type ForwardReadyMsg struct {
	ID         int
	RemotePort int
}

// ForwardFailedMsg is sent when a forwarder could not be established
type ForwardFailedMsg struct {
	ID         int
	RemotePort int
	Error      error
}

// ForwardExitedMsg is sent when an established forwarder's ssh process exits
type ForwardExitedMsg struct {
	ID         int
	RemotePort int
	Error      error
}

// PortForwarder manages a single SSH port forward using the ssh command
type PortForwarder struct {
	hostName   string
	localPort  int
	remotePort int
	sshCmd     *exec.Cmd
	stderr     bytes.Buffer
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
	pf.sshCmd = exec.Command("ssh",
		"-L", fmt.Sprintf("%d:localhost:%d", pf.localPort, pf.remotePort),
		"-N",                             // Don't execute remote command, just forward ports
		"-o", "ExitOnForwardFailure=yes", // Exit if port forwarding fails
		"-o", "BatchMode=yes", // Never prompt; prompts would corrupt the TUI
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30", // Keep connection alive
		"-o", "ServerAliveCountMax=3",
		pf.hostName)
	// Capture stderr so ssh messages don't draw over the TUI and can be shown as errors
	pf.sshCmd.Stderr = &pf.stderr

	if err := pf.sshCmd.Start(); err != nil {
		return fmt.Errorf("failed to start ssh: %w", err)
	}

	go func() {
		pf.exitErr = pf.sshCmd.Wait()
		close(pf.done)
	}()

	return nil
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
		if pf.sshCmd == nil || pf.sshCmd.Process == nil {
			return
		}
		pf.sshCmd.Process.Kill()
		<-pf.done
	})
}

// exitError describes why the ssh process exited. Only valid after done is closed.
func (pf *PortForwarder) exitError() error {
	lines := strings.Split(strings.TrimSpace(pf.stderr.String()), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return fmt.Errorf("%s", last)
	}
	if pf.exitErr != nil {
		return fmt.Errorf("ssh exited: %w", pf.exitErr)
	}
	return fmt.Errorf("ssh exited")
}

// WaitForwardReady waits for a started forwarder to become usable
func WaitForwardReady(id int, pf *PortForwarder) tea.Cmd {
	return func() tea.Msg {
		if err := pf.WaitReady(30 * time.Second); err != nil {
			pf.Stop()
			return ForwardFailedMsg{ID: id, RemotePort: pf.remotePort, Error: err}
		}
		return ForwardReadyMsg{ID: id, RemotePort: pf.remotePort}
	}
}

// WaitForwardExit reports when an established forwarder's ssh process exits
func WaitForwardExit(id int, pf *PortForwarder) tea.Cmd {
	return func() tea.Msg {
		<-pf.Done()
		return ForwardExitedMsg{ID: id, RemotePort: pf.remotePort, Error: pf.exitError()}
	}
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
