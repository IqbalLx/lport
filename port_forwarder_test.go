package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// useFakeSSH puts testdata/fake-ssh first on PATH so forwards run locally
func useFakeSSH(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh is a unix script")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed for the fake ssh")
	}
	dir, err := filepath.Abs("testdata/fake-ssh")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// startServer starts an HTTP server answering with body and returns its port
func startServer(t *testing.T, body string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func get(t *testing.T, port int) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func newTestModel(host string) *Model {
	m := NewModel()
	m.hosts = []SSHHost{{Name: host}}
	m.state = StateSelectPort
	return m
}

// drive runs cmd and feeds its message to the model, returning the follow-up command
func drive(m *Model, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	_, next := m.Update(cmd())
	return next
}

func TestMultipleForwards(t *testing.T) {
	useFakeSSH(t)
	m := newTestModel("good")
	t.Cleanup(m.stopAllForwards)

	ports := []int{startServer(t, "one"), startServer(t, "two")}
	m.ports = ports
	for _, port := range ports {
		key := m.hostKey(port)
		cmd := m.toggleForward(key)
		if got := m.forwards[key].status; got != ForwardStarting {
			t.Fatalf("port %d: status %v, want starting", port, got)
		}
		drive(m, cmd)
		if pf := m.forwards[key]; pf.status != ForwardActive {
			t.Fatalf("port %d: status %v (%v), want active", port, pf.status, pf.err)
		}
	}

	for i, body := range []string{"one", "two"} {
		pf := m.forwards[m.hostKey(ports[i])]
		// The servers hold the remote ports locally, so a different local port is chosen
		if pf.localPort == ports[i] {
			t.Errorf("port %d: expected a fallback local port", ports[i])
		}
		if got := get(t, pf.localPort); got != body {
			t.Errorf("localhost:%d = %q, want %q", pf.localPort, got, body)
		}
	}
	if !strings.Contains(m.renderPortSelection(), "2 port(s) forwarding") {
		t.Error("summary missing from port list")
	}

	// Toggling again stops the ssh process
	key := m.hostKey(ports[0])
	forwarder := m.forwards[key].forwarder
	m.toggleForward(key)
	if m.forwards[key] != nil {
		t.Fatal("forward still tracked after toggling off")
	}
	select {
	case <-forwarder.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ssh still running after toggling off")
	}
}

func TestForwardFailureShowsSSHError(t *testing.T) {
	useFakeSSH(t)
	m := newTestModel("badhost")
	key := m.hostKey(4999)
	drive(m, m.startForward(key))

	pf := m.forwards[key]
	if pf.status != ForwardFailed {
		t.Fatalf("status %v, want failed", pf.status)
	}
	if !strings.Contains(pf.err.Error(), "Connection refused") {
		t.Errorf("error %q should carry ssh's message", pf.err)
	}
}

func TestConnectionLost(t *testing.T) {
	useFakeSSH(t)
	m := newTestModel("good")
	t.Cleanup(m.stopAllForwards)

	key := m.hostKey(startServer(t, "x"))
	exitCmd := drive(m, m.startForward(key))
	pf := m.forwards[key]
	pf.forwarder.process.Kill()
	drive(m, exitCmd)

	if pf.status != ForwardFailed || !strings.Contains(pf.err.Error(), "connection lost") {
		t.Fatalf("status %v err %v, want connection lost", pf.status, pf.err)
	}
}

func TestCancelWhileStarting(t *testing.T) {
	useFakeSSH(t)
	m := newTestModel("good")
	key := m.hostKey(startServer(t, "x"))

	cmd := m.toggleForward(key)
	forwarder := m.forwards[key].forwarder
	m.toggleForward(key)
	drive(m, cmd) // the stale ready/failed message must be ignored

	if m.forwards[key] != nil {
		t.Fatal("stale message brought the forward back")
	}
	select {
	case <-forwarder.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ssh left running after cancel")
	}
}

func TestAdoptExistingForward(t *testing.T) {
	useFakeSSH(t)

	// A forward left behind by a "previous session"
	remotePort := startServer(t, "kept")
	localPort, _, err := findPreferredLocalPort(remotePort)
	if err != nil {
		t.Fatal(err)
	}
	orphan := NewPortForwarder("good", localPort, remotePort)
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(orphan.Stop)
	if err := orphan.WaitReady(10 * time.Second); err != nil {
		t.Fatal(err)
	}

	m := newTestModel("good")
	m.state = StateSelectHost
	m.Update(ExistingForwardsMsg{Forwards: []ExistingForward{{
		PID: orphan.process.Pid, Host: "good", LocalPort: localPort, RemotePort: remotePort,
	}}})

	key := forwardKey{host: "good", remotePort: remotePort}
	if m.state != StateActiveForwards {
		t.Fatalf("state %v, want the active forwards screen", m.state)
	}
	if pf := m.forwards[key]; pf == nil || pf.status != ForwardActive {
		t.Fatal("existing forward not adopted as active")
	}
	if got := get(t, localPort); got != "kept" {
		t.Errorf("adopted forward served %q", got)
	}

	// Closing it from the overview kills the process we didn't start
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if m.forwards[key] != nil {
		t.Fatal("forward still tracked after closing")
	}
	select {
	case <-orphan.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("adopted ssh still running after closing")
	}
}

func TestDetachLeavesForwardsRunning(t *testing.T) {
	useFakeSSH(t)
	m := newTestModel("good")
	key := m.hostKey(startServer(t, "x"))
	drive(m, m.startForward(key))
	forwarder := m.forwards[key].forwarder
	t.Cleanup(forwarder.Stop)

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if _, ok := cmd().(tea.QuitMsg); !ok || !m.detached {
		t.Fatal("d should quit in detached mode")
	}
	if !processAlive(forwarder.process.Pid) {
		t.Fatal("detach killed the forward")
	}
	if got := m.RunningForwards(); len(got) != 1 {
		t.Errorf("RunningForwards = %v", got)
	}
}

func TestQuitStopsForwards(t *testing.T) {
	useFakeSSH(t)
	m := newTestModel("good")
	key := m.hostKey(startServer(t, "x"))
	drive(m, m.startForward(key))
	forwarder := m.forwards[key].forwarder

	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	select {
	case <-forwarder.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("q left ssh running")
	}
}

func TestParseForwardCommand(t *testing.T) {
	tests := []struct {
		cmd    string
		ok     bool
		host   string
		local  int
		remote int
	}{
		{"ssh -L 3000:localhost:3000 -N -o ExitOnForwardFailure=yes -o BatchMode=yes my-host", true, "my-host", 3000, 3000},
		{"/usr/bin/ssh -L 5123:localhost:80 -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 zog", true, "zog", 5123, 80},
		// interactive session with a forward: not ours
		{"ssh -L 3000:localhost:3000 my-host", false, "", 0, 0},
		{"ssh -N -o ExitOnForwardFailure=yes my-host", false, "", 0, 0},
		{"ssh -L 127.0.0.1:3000:localhost:3000 -N -o ExitOnForwardFailure=yes h", false, "", 0, 0},
		{"python3 ssh -L 3000:localhost:3000 -N -o ExitOnForwardFailure=yes h", false, "", 0, 0},
	}
	for _, tt := range tests {
		host, local, remote, ok := parseForwardCommand(strings.Fields(tt.cmd))
		if ok != tt.ok || host != tt.host || local != tt.local || remote != tt.remote {
			t.Errorf("%q = (%q, %d, %d, %v), want (%q, %d, %d, %v)",
				tt.cmd, host, local, remote, ok, tt.host, tt.local, tt.remote, tt.ok)
		}
	}
}

func TestManualPortJoinsList(t *testing.T) {
	useFakeSSH(t)
	m := newTestModel("good")
	t.Cleanup(m.stopAllForwards)
	m.ports = []int{1, 65000}
	port := startServer(t, "x")

	m.state = StateManualPort
	m.manualPort = fmt.Sprint(port)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drive(m, cmd)

	if m.state != StateSelectPort || m.ports[m.cursor] != port {
		t.Fatalf("manual port not selected in list: %v cursor %d", m.ports, m.cursor)
	}
	if pf := m.forwards[m.hostKey(port)]; pf == nil || pf.status != ForwardActive {
		t.Fatal("manual port not forwarded")
	}
}
