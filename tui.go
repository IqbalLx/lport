package main

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// AppState represents the current state of the application
type AppState int

const (
	StateSelectHost AppState = iota
	StateConnecting
	StateSelectPort
	StateManualPort
)

// ForwardStatus is the state of a single port forward
type ForwardStatus int

const (
	ForwardStarting ForwardStatus = iota
	ForwardActive
	ForwardFailed
)

// portForward tracks a forward for one remote port
type portForward struct {
	id        int
	status    ForwardStatus
	localPort int
	forwarder *PortForwarder
	err       error
}

// Model represents the TUI model
type Model struct {
	state       AppState
	sshConfig   *SSHConfig
	hosts       []SSHHost
	selectedHost int
	ports       []int
	cursor      int
	manualPort  string
	forwards    map[int]*portForward // keyed by remote port
	nextID      int
	message     string
	err         error
}

// NewModel creates a new TUI model
func NewModel() *Model {
	return &Model{
		state:     StateSelectHost,
		sshConfig: NewSSHConfig(),
		cursor:    0,
		forwards:  make(map[int]*portForward),
	}
}

// Init initializes the model
func (m *Model) Init() tea.Cmd {
	// Load SSH config
	if err := m.sshConfig.LoadConfig(); err != nil {
		m.err = err
		// Don't quit immediately, let user see the error
		return nil
	}
	m.hosts = m.sshConfig.GetHosts()
	
	// Check if we have any hosts
	if len(m.hosts) == 0 {
		m.err = fmt.Errorf("no SSH hosts found in config file")
		return nil
	}
	
	return nil
}

// Update handles messages and updates the model
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch m.state {
		case StateSelectHost:
			return m.updateHostSelection(msg)
		case StateConnecting:
			return m.updateConnecting(msg)
		case StateSelectPort:
			return m.updatePortSelection(msg)
		case StateManualPort:
			return m.updateManualPort(msg)
		}
	case PortsDetectedMsg:
		// Ignore late results if the user cancelled the connection
		if m.state != StateConnecting {
			return m, nil
		}
		m.ports = msg.Ports
		m.state = StateSelectPort
		m.cursor = 0
		// Set a message about the connection attempt
		if len(msg.Ports) == 0 {
			m.message = fmt.Sprintf("Could not connect to %s or no ports detected", m.hosts[m.selectedHost].Name)
		} else {
			m.message = ""
		}
		return m, nil
	case ForwardReadyMsg:
		pf := m.forwards[msg.RemotePort]
		if pf == nil || pf.id != msg.ID {
			return m, nil
		}
		pf.status = ForwardActive
		return m, WaitForwardExit(pf.id, pf.forwarder)
	case ForwardFailedMsg:
		pf := m.forwards[msg.RemotePort]
		if pf == nil || pf.id != msg.ID {
			return m, nil
		}
		pf.status = ForwardFailed
		pf.err = msg.Error
		return m, nil
	case ForwardExitedMsg:
		pf := m.forwards[msg.RemotePort]
		if pf == nil || pf.id != msg.ID {
			return m, nil
		}
		pf.status = ForwardFailed
		pf.err = fmt.Errorf("connection lost: %v", msg.Error)
		return m, nil
	case ErrorMsg:
		m.message = fmt.Sprintf("Error: %v", msg.Error)
		return m, nil
	}
	return m, nil
}

// updateHostSelection handles host selection state
func (m *Model) updateHostSelection(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.hosts)-1 {
			m.cursor++
		}
	case "enter", "space":
		m.selectedHost = m.cursor
		m.state = StateConnecting
		m.message = fmt.Sprintf("Connecting to %s...", m.hosts[m.selectedHost].Name)
		// Detect ports on selected host
		return m, DetectPorts(m.hosts[m.selectedHost])
	case "m":
		// Manual port forwarding
		m.selectedHost = m.cursor
		m.ports = nil
		m.state = StateManualPort
		m.manualPort = ""
		m.message = ""
		return m, nil
	}
	return m, nil
}

// updateConnecting handles connecting state
func (m *Model) updateConnecting(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		m.state = StateSelectHost
		m.cursor = m.selectedHost
		m.message = ""
		return m, nil
	}
	return m, nil
}

// updatePortSelection handles port selection state
func (m *Model) updatePortSelection(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.stopAllForwards()
		return m, tea.Quit
	case "esc":
		m.stopAllForwards()
		m.state = StateSelectHost
		m.cursor = m.selectedHost
		m.message = ""
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.ports)-1 {
			m.cursor++
		}
	case "enter", "space":
		if len(m.ports) == 0 {
			return m, nil
		}
		return m, m.toggleForward(m.ports[m.cursor])
	case "m":
		// Manual port forwarding
		m.state = StateManualPort
		m.manualPort = ""
		m.message = ""
		return m, nil
	}
	return m, nil
}

// updateManualPort handles manual port input state
func (m *Model) updateManualPort(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.stopAllForwards()
		return m, tea.Quit
	case "esc":
		m.message = ""
		if len(m.ports) > 0 {
			m.state = StateSelectPort
		} else {
			m.state = StateSelectHost
		}
		return m, nil
	case "enter":
		if m.manualPort == "" {
			return m, nil
		}
		port, err := strconv.Atoi(m.manualPort)
		if err != nil || port <= 0 || port > 65535 {
			m.message = "Port number must be between 1 and 65535"
			return m, nil
		}
		m.message = ""
		m.addPort(port)
		m.state = StateSelectPort
		if pf := m.forwards[port]; pf != nil && pf.status != ForwardFailed {
			// Already forwarding or starting
			return m, nil
		}
		return m, m.startForward(port)
	case "backspace":
		if len(m.manualPort) > 0 {
			m.manualPort = m.manualPort[:len(m.manualPort)-1]
		}
	default:
		// Add character to manual port
		if len(msg.String()) == 1 && msg.String() >= "0" && msg.String() <= "9" {
			m.manualPort += msg.String()
		}
	}
	return m, nil
}

// addPort adds a port to the list if missing and moves the cursor to it
func (m *Model) addPort(port int) {
	i := sort.SearchInts(m.ports, port)
	if i == len(m.ports) || m.ports[i] != port {
		m.ports = slices.Insert(m.ports, i, port)
	}
	m.cursor = i
}

// toggleForward starts forwarding a port, or stops it if already forwarded
func (m *Model) toggleForward(port int) tea.Cmd {
	if pf := m.forwards[port]; pf != nil && pf.status != ForwardFailed {
		m.stopForward(port)
		return nil
	}
	return m.startForward(port)
}

// startForward spawns ssh for a port and returns a command that waits for it to be ready
func (m *Model) startForward(port int) tea.Cmd {
	m.nextID++
	entry := &portForward{id: m.nextID, status: ForwardStarting}
	m.forwards[port] = entry

	localPort, _, err := findPreferredLocalPort(port)
	if err != nil {
		entry.status = ForwardFailed
		entry.err = fmt.Errorf("no available local port: %w", err)
		return nil
	}
	entry.localPort = localPort

	forwarder := NewPortForwarder(m.hosts[m.selectedHost].Name, localPort, port)
	if err := forwarder.Start(); err != nil {
		entry.status = ForwardFailed
		entry.err = err
		return nil
	}
	entry.forwarder = forwarder

	return WaitForwardReady(entry.id, forwarder)
}

// stopForward stops and forgets the forward for a port
func (m *Model) stopForward(port int) {
	if pf := m.forwards[port]; pf != nil && pf.forwarder != nil {
		pf.forwarder.Stop()
	}
	delete(m.forwards, port)
}

// stopAllForwards stops every forward
func (m *Model) stopAllForwards() {
	for port := range m.forwards {
		m.stopForward(port)
	}
}

// View renders the TUI
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render builds the TUI content
func (m *Model) render() string {
	if m.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF5F87")).
			Bold(true)
		
		return fmt.Sprintf("%s\n\n%s\n\nPress q to quit.", 
			errorStyle.Render("❌ Error"), m.err.Error())
	}

	var s strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		Background(lipgloss.Color("#7D56F4")).
		Padding(0, 1)

	s.WriteString(headerStyle.Render("lport - SSH Port Forwarder"))
	s.WriteString("\n\n")

	switch m.state {
	case StateSelectHost:
		s.WriteString(m.renderHostSelection())
	case StateConnecting:
		s.WriteString(m.renderConnecting())
	case StateSelectPort:
		s.WriteString(m.renderPortSelection())
	case StateManualPort:
		s.WriteString(m.renderManualPort())
	}

	return s.String()
}

// renderHostSelection renders the host selection view
func (m *Model) renderHostSelection() string {
	var s strings.Builder
	
	s.WriteString("Select an SSH host:\n\n")

	for i, host := range m.hosts {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}

		hostInfo := fmt.Sprintf("%s@%s", host.User, host.Hostname)
		if host.User == "" {
			hostInfo = host.Hostname
		}

		style := lipgloss.NewStyle()
		if m.cursor == i {
			style = style.Foreground(lipgloss.Color("#FF75B7"))
		}

		s.WriteString(fmt.Sprintf("%s %s (%s)\n", cursor, 
			style.Render(host.Name), hostInfo))
	}

	s.WriteString("\n")
	s.WriteString("Controls:\n")
	s.WriteString("  ↑/↓: Navigate  Enter: Select  m: Manual port  q: Quit\n")

	return s.String()
}

// renderConnecting renders the connecting view
func (m *Model) renderConnecting() string {
	var s strings.Builder
	
	connectingStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#00BFFF")).
		Bold(true)
	
	s.WriteString(connectingStyle.Render("🔄 " + m.message))
	s.WriteString("\n\n")
	s.WriteString("Please wait while connecting to the remote host...\n\n")
	s.WriteString("Controls:\n")
	s.WriteString("  Esc: Cancel and go back  q: Quit\n")

	return s.String()
}

// renderPortSelection renders the port selection view
func (m *Model) renderPortSelection() string {
	var s strings.Builder
	
	host := m.hosts[m.selectedHost]
	s.WriteString(fmt.Sprintf("Ports on %s:\n\n", host.Name))

	if len(m.ports) == 0 {
		if m.message != "" {
			warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFA500"))
			s.WriteString(warningStyle.Render("⚠️  " + m.message))
			s.WriteString("\n\n")
		}
		s.WriteString("No open ports detected.\n\n")
		s.WriteString("Press 'm' for manual port forwarding or Esc to go back.\n")
		return s.String()
	}

	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	startingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF"))
	failedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))

	active := 0
	for i, port := range m.ports {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}

		style := lipgloss.NewStyle()
		if m.cursor == i {
			style = style.Foreground(lipgloss.Color("#FF75B7"))
		}

		checkbox := "[ ]"
		status := ""
		if pf := m.forwards[port]; pf != nil {
			switch pf.status {
			case ForwardStarting:
				checkbox = "[x]"
				status = startingStyle.Render("◌ connecting...")
			case ForwardActive:
				active++
				checkbox = "[x]"
				status = activeStyle.Render(fmt.Sprintf("● localhost:%d", pf.localPort))
				if pf.localPort != port {
					status += dimStyle.Render(fmt.Sprintf(" (port %d was in use)", port))
				}
			case ForwardFailed:
				status = failedStyle.Render(fmt.Sprintf("✗ %v", pf.err))
			}
		}

		s.WriteString(fmt.Sprintf("%s %s %s  %s\n", cursor, style.Render(checkbox),
			style.Render(fmt.Sprintf("Port %-5d", port)), status))
	}

	s.WriteString("\n")
	if active > 0 {
		s.WriteString(activeStyle.Render(fmt.Sprintf("✓ %d port(s) forwarding", active)))
		s.WriteString("\n\n")
	}
	s.WriteString("Controls:\n")
	s.WriteString("  ↑/↓: Navigate  Space/Enter: Toggle forward  m: Add port\n")
	s.WriteString("  Esc: Stop all & back  q: Stop all & quit\n")

	return s.String()
}

// renderManualPort renders the manual port input view
func (m *Model) renderManualPort() string {
	var s strings.Builder
	
	host := m.hosts[m.selectedHost]
	hostStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7D56F4")).
		Bold(true)
	
	s.WriteString(fmt.Sprintf("Manual port forwarding for %s:\n\n", hostStyle.Render(host.Name)))
	
	// Label for the input
	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FAFAFA")).
		Bold(true)
	
	s.WriteString(labelStyle.Render("Remote port number:"))
	s.WriteString("\n\n")
	
	// Input box styling
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(0, 1).
		Width(20).
		Align(lipgloss.Left)
	
	// Show placeholder or current input
	displayText := m.manualPort
	if displayText == "" {
		placeholderStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			Italic(true)
		displayText = placeholderStyle.Render("e.g., 3000")
	}
	
	s.WriteString(inputStyle.Render(displayText))
	s.WriteString("\n\n")
	
	// Add cursor indicator
	if m.manualPort != "" {
		cursorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF75B7"))
		s.WriteString(cursorStyle.Render("│"))
		s.WriteString("\n\n")
	} else {
		s.WriteString("\n")
	}

	if m.message != "" {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87"))
		s.WriteString(errorStyle.Render("✗ " + m.message))
		s.WriteString("\n\n")
	}
	
	s.WriteString("Controls:\n")
	s.WriteString("  0-9: Enter digits  Backspace: Delete  Enter: Start forwarding\n")
	s.WriteString("  Esc: Back  q: Quit\n")

	return s.String()
}
