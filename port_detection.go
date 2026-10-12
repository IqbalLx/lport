package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// PortsDetectedMsg is sent when ports are detected
type PortsDetectedMsg struct {
	Ports []int
}

// ErrorMsg is sent when an error occurs
type ErrorMsg struct {
	Error error
}

// DetectPorts detects open ports on the remote host
func DetectPorts(host SSHHost) tea.Cmd {
	return func() tea.Msg {
		ports, err := detectRemotePorts(host)
		if err != nil {
			// Log the error for debugging but don't quit the app
			fmt.Fprintf(os.Stderr, "Debug: Port detection failed for %s: %v\n", host.Name, err)
			// Return empty ports list so user can still use manual port forwarding
			return PortsDetectedMsg{Ports: []int{}}
		}
		fmt.Fprintf(os.Stderr, "Debug: Detected %d ports on %s: %v\n", len(ports), host.Name, ports)
		return PortsDetectedMsg{Ports: ports}
	}
}

// detectRemotePorts connects to the remote host and detects open ports using ssh command
func detectRemotePorts(host SSHHost) ([]int, error) {
	// Try different commands to detect listening ports
	commands := []string{
		"netstat -tlnp 2>/dev/null | grep LISTEN | awk '{print $4}' | cut -d: -f2 | sort -n | uniq",
		"ss -tlnp 2>/dev/null | grep LISTEN | awk '{print $4}' | cut -d: -f2 | sort -n | uniq",
		"lsof -i -P -n 2>/dev/null | grep LISTEN | awk '{print $9}' | cut -d: -f2 | sort -n | uniq",
	}

	var output []byte
	var err error

	for _, cmd := range commands {
		fmt.Fprintf(os.Stderr, "Debug: Running command on %s: %s\n", host.Name, cmd)
		
		// Use ssh command directly - this supports all SSH features including ProxyCommand
		sshCmd := exec.Command("ssh", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", host.Name, cmd)
		
		output, err = sshCmd.Output()
		if err == nil && len(output) > 0 {
			fmt.Fprintf(os.Stderr, "Debug: Command succeeded, got output\n")
			break
		}
		fmt.Fprintf(os.Stderr, "Debug: Command failed: %v\n", err)
	}

	if err != nil || len(output) == 0 {
		fmt.Fprintf(os.Stderr, "Debug: All port detection commands failed, trying common ports\n")
		// Fallback: try common ports
		return detectCommonPorts(host), nil
	}

	// Parse the output to extract port numbers
	ports := make([]int, 0)
	lines := strings.Split(string(output), "\n")
	
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		
		port, err := strconv.Atoi(line)
		if err == nil && port > 0 && port < 65536 {
			ports = append(ports, port)
		}
	}

	// Remove duplicates and sort
	ports = removeDuplicates(ports)
	sort.Ints(ports)

	return ports, nil
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



// removeDuplicates removes duplicate integers from a slice
func removeDuplicates(slice []int) []int {
	keys := make(map[int]bool)
	result := make([]int, 0)
	
	for _, item := range slice {
		if !keys[item] {
			keys[item] = true
			result = append(result, item)
		}
	}
	
	return result
}