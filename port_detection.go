package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// PortsDetectedMsg is sent when ports are detected
type PortsDetectedMsg struct {
	Ports []int
	// Programs maps a port to the program listening on it, when the remote
	// host reveals it (usually only for processes owned by the SSH user)
	Programs map[int]string
}

// ErrorMsg is sent when an error occurs
type ErrorMsg struct {
	Error error
}

// DetectPorts detects open ports on the remote host
func DetectPorts(host SSHHost) tea.Cmd {
	return func() tea.Msg {
		ports, programs, err := detectRemotePorts(host)
		if err != nil {
			// Return empty ports list so user can still use manual port forwarding
			return PortsDetectedMsg{Ports: []int{}}
		}
		return PortsDetectedMsg{Ports: ports, Programs: programs}
	}
}

// listenersScript lists listening TCP sockets with the first tool available,
// then the ports published by containers: those are owned by root's
// docker-proxy, so ss can't name them, but docker ps can if the user is in
// the docker group. Each part prints a marker so the output can be parsed in
// the right format.
const listenersScript = `(echo '#ss'; ss -tlnp) 2>/dev/null ||
(echo '#netstat'; netstat -tlnp) 2>/dev/null ||
(echo '#lsof'; lsof -nP -iTCP -sTCP:LISTEN) 2>/dev/null
(echo '#docker'; docker ps --format '{{.Names}}|{{.Ports}}') 2>/dev/null
(echo '#podman'; podman ps --format '{{.Names}}|{{.Ports}}') 2>/dev/null
true`

// detectRemotePorts connects to the remote host and detects open ports using ssh command
func detectRemotePorts(host SSHHost) ([]int, map[int]string, error) {
	// Use ssh command directly - this supports all SSH features including ProxyCommand
	sshCmd := exec.Command("ssh", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", host.Name, listenersScript)
	output, _ := sshCmd.Output() // the script exits non-zero when the last tool is missing

	ports, programs := parseListeners(string(output))
	if len(ports) == 0 {
		// Fallback: try common ports
		return detectCommonPorts(host), nil, nil
	}
	return ports, programs, nil
}

var ssProgramPattern = regexp.MustCompile(`users:\(\("([^"]+)"`)

// parseListeners parses the output of listenersScript into sorted ports and
// the programs listening on them
func parseListeners(output string) ([]int, map[int]string) {
	names := make(map[int][]string)
	containers := make(map[int][]string)
	format := ""

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "#") {
			format = strings.TrimSpace(line[1:])
			continue
		}
		fields := strings.Fields(line)

		var address, program string
		switch format {
		case "docker", "podman":
			// imigrasi_pgsql|0.0.0.0:5432->5432/tcp, [::]:5432->5432/tcp
			name, ports, ok := strings.Cut(line, "|")
			if !ok {
				continue
			}
			for _, port := range parsePublishedPorts(ports) {
				label := format + ":" + name
				if !slices.Contains(containers[port], label) {
					containers[port] = append(containers[port], label)
				}
			}
			continue
		case "ss":
			// LISTEN 0 511 127.0.0.1:6379 0.0.0.0:* users:(("redis-server",pid=1,fd=6))
			if len(fields) < 4 || fields[0] != "LISTEN" {
				continue
			}
			address = fields[3]
			if m := ssProgramPattern.FindStringSubmatch(line); m != nil {
				program = m[1]
			}
		case "netstat":
			// tcp 0 0 127.0.0.1:6379 0.0.0.0:* LISTEN 1234/redis-server
			if len(fields) < 6 || !strings.HasPrefix(fields[0], "tcp") || fields[5] != "LISTEN" {
				continue
			}
			address = fields[3]
			if len(fields) > 6 {
				if _, name, ok := strings.Cut(fields[6], "/"); ok {
					program = name
				}
			}
		case "lsof":
			// redis-ser 1234 user 6u IPv4 0x1 0t0 TCP 127.0.0.1:6379 (LISTEN)
			if len(fields) < 10 || fields[len(fields)-1] != "(LISTEN)" {
				continue
			}
			address = fields[len(fields)-2]
			program = strings.ReplaceAll(fields[0], `\x20`, " ")
		default:
			continue
		}

		// The port follows the last colon: 0.0.0.0:80, [::]:80, *:80, 127.0.0.53%lo:53
		i := strings.LastIndex(address, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(address[i+1:])
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		if _, seen := names[port]; !seen {
			names[port] = nil
		}
		if program != "" && !slices.Contains(names[port], program) {
			names[port] = append(names[port], program)
		}
	}

	// A container name says more than docker-proxy (or nothing at all), and
	// published ports are reachable even when no host process is listening
	for port, labels := range containers {
		names[port] = labels
	}

	ports := make([]int, 0, len(names))
	programs := make(map[int]string)
	for port, list := range names {
		ports = append(ports, port)
		if len(list) > 0 {
			programs[port] = strings.Join(list, ",")
		}
	}
	sort.Ints(ports)
	return ports, programs
}

// detectCommonPorts tries to detect common ports by testing connections through SSH
func detectCommonPorts(host SSHHost) []int {
	commonPorts := []int{80, 443, 3000, 3001, 4000, 5000, 8000, 8080, 8443, 9000}
	var openPorts []int

	fmt.Fprintf(os.Stderr, "Debug: Testing common ports on %s\n", host.Name)

	for _, port := range commonPorts {
		// Test if port is open using SSH to run a quick connection test
		cmd := fmt.Sprintf("timeout 1 bash -c '</dev/tcp/localhost/%d' 2>/dev/null && echo 'open' || echo 'closed'", port)
		sshCmd := exec.Command("ssh", "-o", "ConnectTimeout=5", "-o", "BatchMode=yes", host.Name, cmd)

		output, err := sshCmd.Output()
		if err == nil && strings.TrimSpace(string(output)) == "open" {
			openPorts = append(openPorts, port)
			fmt.Fprintf(os.Stderr, "Debug: Port %d is open\n", port)
		}
	}

	return openPorts
}

// parsePublishedPorts returns the host TCP ports in a docker/podman ps Ports
// column, e.g. "0.0.0.0:4317-4318->4317-4318/tcp, [::]:80->8501/tcp, 3200/tcp".
// Exposed but unpublished ports (no "->") aren't reachable from the host.
func parsePublishedPorts(column string) []int {
	var ports []int
	for _, mapping := range strings.Split(column, ",") {
		hostSide, containerSide, ok := strings.Cut(strings.TrimSpace(mapping), "->")
		if !ok || !strings.HasSuffix(containerSide, "/tcp") {
			continue
		}
		hostPorts := hostSide[strings.LastIndex(hostSide, ":")+1:]
		first, last, isRange := strings.Cut(hostPorts, "-")
		if !isRange {
			last = first
		}
		lo, err1 := strconv.Atoi(first)
		hi, err2 := strconv.Atoi(last)
		if err1 != nil || err2 != nil || lo <= 0 || hi > 65535 || hi < lo || hi-lo > 1000 {
			continue
		}
		for port := lo; port <= hi; port++ {
			if !slices.Contains(ports, port) {
				ports = append(ports, port)
			}
		}
	}
	return ports
}
