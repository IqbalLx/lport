package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(version)
		return
	}

	// Check for test mode
	if len(os.Args) > 1 && os.Args[1] == "--test" {
		testMode()
		return
	}

	// Check for connection test mode
	if len(os.Args) > 2 && os.Args[1] == "--test-connect" {
		testConnection(os.Args[2])
		return
	}

	// Check for port mapping test mode
	if len(os.Args) > 2 && os.Args[1] == "--test-port" {
		testPortMapping(os.Args[2])
		return
	}

	// Initialize the application
	app := NewApp()
	if err := app.Run(); err != nil {
		fmt.Printf("Error running application: %v\n", err)
		os.Exit(1)
	}
}

// testMode runs a simple test without TUI
func testMode() {
	fmt.Println("lport - SSH Port Forwarder - Test Mode")
	fmt.Println("======================================")

	// Test SSH config loading
	config := NewSSHConfig()
	if err := config.LoadConfig(); err != nil {
		fmt.Printf("❌ Failed to load SSH config: %v\n", err)
		return
	}

	hosts := config.GetHosts()
	fmt.Printf("✅ Successfully loaded SSH config with %d hosts:\n\n", len(hosts))

	for i, host := range hosts {
		fmt.Printf("%d. %s\n", i+1, host.Name)
		fmt.Printf("   Host: %s\n", host.Hostname)
		fmt.Printf("   User: %s\n", host.User)
		fmt.Printf("   Port: %s\n", host.Port)
		if host.Identity != "" {
			fmt.Printf("   Identity: %s\n", host.Identity)
		}
		fmt.Println()
	}

	fmt.Println("📝 Note: The example hosts above are not real servers.")
	fmt.Println("   Replace them in ~/.ssh/config with your actual SSH hosts.")
	fmt.Println("")
	fmt.Println("To run the interactive TUI, use: ./lport")
	fmt.Println("Note: TUI requires a proper terminal environment")
	fmt.Println("")
	fmt.Println("To test connection to a specific host: ./lport --test-connect <hostname>")
	fmt.Println("To test port mapping logic: ./lport --test-port <port>")
}

// testConnection tests connecting to a specific host
func testConnection(hostName string) {
	fmt.Printf("Testing connection to host: %s\n", hostName)
	fmt.Println("=====================================")

	// Load SSH config
	config := NewSSHConfig()
	if err := config.LoadConfig(); err != nil {
		fmt.Printf("❌ Failed to load SSH config: %v\n", err)
		return
	}

	// Find the host
	host, err := config.GetHostByName(hostName)
	if err != nil {
		fmt.Printf("❌ Host not found: %v\n", err)
		return
	}

	fmt.Printf("Found host configuration:\n")
	fmt.Printf("  Name: %s\n", host.Name)
	fmt.Printf("  Hostname: %s\n", host.Hostname)
	fmt.Printf("  User: %s\n", host.User)
	fmt.Printf("  Port: %s\n", host.Port)
	if host.Identity != "" {
		fmt.Printf("  Identity: %s\n", host.Identity)
	}
	fmt.Println("")

	// Expand shell variables in the host config
	expandedHost := *host
	expandedHost.User = expandShellVars(host.User)
	expandedHost.Identity = expandShellVars(host.Identity)

	if expandedHost.User != host.User {
		fmt.Printf("Expanded user: %s -> %s\n", host.User, expandedHost.User)
	}
	if expandedHost.Identity != host.Identity {
		fmt.Printf("Expanded identity: %s -> %s\n", host.Identity, expandedHost.Identity)
	}
	if expandedHost.User != host.User || expandedHost.Identity != host.Identity {
		fmt.Println("")
	}

	// Test SSH connection using ssh command (supports all SSH features)
	fmt.Println("Testing SSH connection...")
	fmt.Printf("Running: ssh -o ConnectTimeout=10 -o BatchMode=yes %s echo 'connection test'\n", expandedHost.Name)

	sshCmd := exec.Command("ssh", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", expandedHost.Name, "echo", "connection test")
	output, err := sshCmd.Output()
	if err != nil {
		fmt.Printf("❌ SSH connection failed: %v\n", err)
		fmt.Println("")
		fmt.Println("Common SSH connection issues:")
		fmt.Println("- SSH keys not set up or not in SSH agent")
		fmt.Println("- Wrong username or hostname")
		fmt.Println("- Host key verification failed")
		fmt.Println("- SSH server not running or configured differently")
		fmt.Println("- ProxyCommand or other SSH config issues")
		fmt.Println("")
		fmt.Println("Try running the SSH command manually:")
		fmt.Printf("  ssh %s\n", expandedHost.Name)
		return
	}

	if strings.TrimSpace(string(output)) == "connection test" {
		fmt.Printf("✅ SSH connection successful!\n")
	} else {
		fmt.Printf("⚠️  SSH connection partially successful but got unexpected output: %s\n", string(output))
	}
	fmt.Println("")

	// Test port detection
	fmt.Println("Testing port detection...")
	ports, err := detectRemotePorts(expandedHost)
	if err != nil {
		fmt.Printf("❌ Port detection failed: %v\n", err)
		fmt.Println("")
		fmt.Println("This is expected if:")
		fmt.Println("- The host is not reachable")
		fmt.Println("- SSH keys are not set up")
		fmt.Println("- SSH agent is not running")
		fmt.Println("- The host doesn't exist")
	} else {
		fmt.Printf("✅ Port detection successful! Found %d ports: %v\n", len(ports), ports)
	}

	fmt.Println("")
	fmt.Println("You can still use manual port forwarding in the TUI even if port detection fails.")
}

// expandShellVars expands shell variables in SSH config values
func expandShellVars(value string) string {
	if value == "" {
		return value
	}

	// Handle $(whoami) and $USER
	if strings.Contains(value, "$(whoami)") || strings.Contains(value, "$USER") {
		currentUser, err := user.Current()
		if err == nil {
			value = strings.ReplaceAll(value, "$(whoami)", currentUser.Username)
			value = strings.ReplaceAll(value, "$USER", currentUser.Username)
		}
	}

	// Handle $HOME and ~/
	if strings.Contains(value, "$HOME") || strings.HasPrefix(value, "~/") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			value = strings.ReplaceAll(value, "$HOME", homeDir)
			if strings.HasPrefix(value, "~/") {
				value = strings.Replace(value, "~/", homeDir+"/", 1)
			}
		}
	}

	return value
}

// testPortMapping tests the port mapping logic
func testPortMapping(portStr string) {
	fmt.Printf("Testing port mapping for port: %s\n", portStr)
	fmt.Println("=====================================")

	remotePort, err := strconv.Atoi(portStr)
	if err != nil {
		fmt.Printf("❌ Invalid port number: %s\n", portStr)
		return
	}

	if remotePort <= 0 || remotePort > 65535 {
		fmt.Printf("❌ Port number must be between 1 and 65535\n")
		return
	}

	// Test the port mapping logic
	localPort, samePort, err := findPreferredLocalPort(remotePort)
	if err != nil {
		fmt.Printf("❌ Failed to find available port: %v\n", err)
		return
	}

	if samePort {
		fmt.Printf("✅ Port %d is available locally - using same port\n", localPort)
		fmt.Printf("   Mapping: localhost:%d -> remote:%d\n", localPort, remotePort)
	} else {
		fmt.Printf("⚠️  Port %d is unavailable locally - using alternative port %d\n", remotePort, localPort)
		fmt.Printf("   Mapping: localhost:%d -> remote:%d\n", localPort, remotePort)
	}

	fmt.Println("")
	fmt.Println("This is how lport will map the ports when forwarding.")
}
