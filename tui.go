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
	StateActiveForwards
)

// ForwardStatus is the state of a single port forward
type ForwardStatus int

const (
	ForwardStarting ForwardStatus = iota
	ForwardActive
	ForwardFailed
)

// portForward tracks a forward for one host and remote port
type portForward struct {
	id        int
	status    ForwardStatus
	localPort int
	forwarder *PortForwarder
	err       error
}

// Model represents the TUI model
type Model struct {
	state        AppState
	sshConfig    *SSHConfig
	hosts        []SSHHost
	selectedHost int
	ports        []int
	cursor       int
	manualPort   string
	forwards     map[forwardKey]*portForward
	programs     map[forwardKey]string // program listening on each detected port
	overview     []forwardKey          // rows of the active forwards screen
	nextID       int
	detached     bool
	message      string
	err          error
}

// NewModel creates a new TUI model
func NewModel() *Model {
	return &Model{
		state:     StateSelectHost,
		sshConfig: NewSSHConfig(),
		cursor:    0,
		forwards:  make(map[forwardKey]*portForward),
		programs:  make(map[forwardKey]string),
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

	// Pick up forwards left running by a previous, detached session
	return DiscoverForwards()
}

// Update handles messages and updates the model
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			// Hard quit: close every forward
			m.stopAllForwards()
			return m, tea.Quit
		case "d":
			// Detach: quit but leave forwards running
			if m.state != StateManualPort {
				m.detached = true
				return m, tea.Quit
			}
		}

		switch m.state {
		case StateSelectHost:
			return m.updateHostSelection(msg)
		case StateConnecting:
			return m.updateConnecting(msg)
		case StateSelectPort:
			return m.updatePortSelection(msg)
		case StateManualPort:
			return m.updateManualPort(msg)
		case StateActiveForwards:
			return m.updateActiveForwards(msg)
		}
	case PortsDetectedMsg:
		// Ignore late results if the user cancelled the connection
		if m.state != StateConnecting {
			return m, nil
		}
		m.ports = msg.Ports
		hostName := m.hosts[m.selectedHost].Name
		for key := range m.programs {
			if key.host == hostName {
				delete(m.programs, key)
			}
		}
		for port, program := range msg.Programs {
			m.programs[forwardKey{host: hostName, remotePort: port}] = program
		}
		// Also list ports already forwarded for this host, even if not detected
		for key := range m.forwards {
			if key.host == hostName {
				m.insertPort(key.remotePort)
			}
		}
		m.state = StateSelectPort
		m.cursor = 0
		// Set a message about the connection attempt
		if len(msg.Ports) == 0 {
			m.message = fmt.Sprintf("Could not connect to %s or no ports detected", hostName)
		} else {
			m.message = ""
		}
		return m, nil
	case ExistingForwardsMsg:
		var cmds []tea.Cmd
		for _, ef := range msg.Forwards {
			key := forwardKey{host: ef.Host, remotePort: ef.RemotePort}
			if m.forwards[key] != nil {
				continue
			}
			m.nextID++
			forwarder := AdoptPortForwarder(ef.Host, ef.LocalPort, ef.RemotePort, ef.PID)
			m.forwards[key] = &portForward{
				id:        m.nextID,
				status:    ForwardActive,
				localPort: ef.LocalPort,
				forwarder: forwarder,
			}
			cmds = append(cmds, WaitForwardExit(m.nextID, key, forwarder))
		}
		if len(cmds) > 0 && m.state == StateSelectHost {
			m.openActiveForwards()
			m.message = fmt.Sprintf("Found %d forward(s) still running from a previous session", len(cmds))
		}
		return m, tea.Batch(cmds...)
	case ForwardReadyMsg:
		pf := m.forwards[msg.Key]
		if pf == nil || pf.id != msg.ID {
			return m, nil
		}
		pf.status = ForwardActive
		return m, WaitForwardExit(pf.id, msg.Key, pf.forwarder)
	case ForwardFailedMsg:
		pf := m.forwards[msg.Key]
		if pf == nil || pf.id != msg.ID {
			return m, nil
		}
		pf.status = ForwardFailed
		pf.err = msg.Error
		return m, nil
	case ForwardExitedMsg:
		pf := m.forwards[msg.Key]
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
	case "a":
		if len(m.forwards) == 0 {
			m.message = "No active forwards"
			return m, nil
		}
		m.openActiveForwards()
		m.message = ""
	}
	return m, nil
}

// updateConnecting handles connecting state
func (m *Model) updateConnecting(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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
	case "esc":
		// Forwards keep running; they're listed on the host screen
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
		return m, m.toggleForward(m.hostKey(m.ports[m.cursor]))
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
		m.cursor = m.insertPort(port)
		m.state = StateSelectPort
		key := m.hostKey(port)
		if pf := m.forwards[key]; pf != nil && pf.status != ForwardFailed {
			// Already forwarding or starting
			return m, nil
		}
		return m, m.startForward(key)
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

// updateActiveForwards handles the active forwards overview state
func (m *Model) updateActiveForwards(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.state = StateSelectHost
		m.cursor = 0
		m.message = ""
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.overview)-1 {
			m.cursor++
		}
	case "enter", "space":
		if len(m.overview) == 0 {
			return m, nil
		}
		return m, m.toggleForward(m.overview[m.cursor])
	}
	return m, nil
}

// openActiveForwards shows the overview of all forwards across hosts
func (m *Model) openActiveForwards() {
	m.overview = m.overview[:0]
	for key := range m.forwards {
		m.overview = append(m.overview, key)
	}
	sort.Slice(m.overview, func(i, j int) bool {
		a, b := m.overview[i], m.overview[j]
		if a.host != b.host {
			return a.host < b.host
		}
		return a.remotePort < b.remotePort
	})
	m.state = StateActiveForwards
	m.cursor = 0
}

// hostKey returns the forward key for a port on the selected host
func (m *Model) hostKey(port int) forwardKey {
	return forwardKey{host: m.hosts[m.selectedHost].Name, remotePort: port}
}

// insertPort adds a port to the sorted list if missing and returns its index
func (m *Model) insertPort(port int) int {
	i := sort.SearchInts(m.ports, port)
	if i == len(m.ports) || m.ports[i] != port {
		m.ports = slices.Insert(m.ports, i, port)
	}
	return i
}

// toggleForward starts forwarding a port, or stops it if already forwarded
func (m *Model) toggleForward(key forwardKey) tea.Cmd {
	if pf := m.forwards[key]; pf != nil && pf.status != ForwardFailed {
		m.stopForward(key)
		return nil
	}
	return m.startForward(key)
}

// startForward spawns ssh for a port and returns a command that waits for it to be ready
func (m *Model) startForward(key forwardKey) tea.Cmd {
	m.nextID++
	entry := &portForward{id: m.nextID, status: ForwardStarting}
	m.forwards[key] = entry

	localPort, _, err := findPreferredLocalPort(key.remotePort)
	if err != nil {
		entry.status = ForwardFailed
		entry.err = fmt.Errorf("no available local port: %w", err)
		return nil
	}
	entry.localPort = localPort

	forwarder := NewPortForwarder(key.host, localPort, key.remotePort)
	if err := forwarder.Start(); err != nil {
		entry.status = ForwardFailed
		entry.err = err
		return nil
	}
	entry.forwarder = forwarder

	return WaitForwardReady(entry.id, key, forwarder)
}

// stopForward stops and forgets a forward
func (m *Model) stopForward(key forwardKey) {
	if pf := m.forwards[key]; pf != nil && pf.forwarder != nil {
		pf.forwarder.Stop()
	}
	delete(m.forwards, key)
}

// stopAllForwards stops every forward
func (m *Model) stopAllForwards() {
	for key := range m.forwards {
		m.stopForward(key)
	}
}

// RunningForwards describes forwards that are up, for the detach summary
func (m *Model) RunningForwards() []string {
	var lines []string
	for key, pf := range m.forwards {
		if pf.status == ForwardActive {
			lines = append(lines, fmt.Sprintf("%s:%d -> localhost:%d", key.host, key.remotePort, pf.localPort))
		}
	}
	sort.Strings(lines)
	return lines
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
	case StateActiveForwards:
		s.WriteString(m.renderActiveForwards())
	}

	return s.String()
}

var (
	activeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	startingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF"))
	failedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
)

// renderForwardStatus returns the checkbox and status text for a forward
func (m *Model) renderForwardStatus(key forwardKey) (checkbox, status string, active bool) {
	pf := m.forwards[key]
	if pf == nil {
		return "[ ]", "", false
	}
	switch pf.status {
	case ForwardStarting:
		return "[x]", startingStyle.Render("◌ connecting..."), false
	case ForwardActive:
		status = activeStyle.Render(fmt.Sprintf("● localhost:%d", pf.localPort))
		if pf.localPort != key.remotePort {
			status += dimStyle.Render(fmt.Sprintf(" (port %d was in use)", key.remotePort))
		}
		return "[x]", status, true
	default:
		return "[ ]", failedStyle.Render(fmt.Sprintf("✗ %v", pf.err)), false
	}
}

// activeCount counts forwards that are up, optionally for a single host
func (m *Model) activeCount(host string) int {
	n := 0
	for key, pf := range m.forwards {
		if pf.status == ForwardActive && (host == "" || key.host == host) {
			n++
		}
	}
	return n
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

		line := fmt.Sprintf("%s %s (%s)", cursor, style.Render(host.Name), hostInfo)
		if n := m.activeCount(host.Name); n > 0 {
			line += "  " + activeStyle.Render(fmt.Sprintf("● %d forwarding", n))
		}
		s.WriteString(line + "\n")
	}

	s.WriteString("\n")
	if m.message != "" {
		s.WriteString(dimStyle.Render(m.message))
		s.WriteString("\n\n")
	}
	if n := m.activeCount(""); n > 0 {
		s.WriteString(activeStyle.Render(fmt.Sprintf("✓ %d port(s) forwarding", n)))
		s.WriteString("\n\n")
	}
	s.WriteString("Controls:\n")
	s.WriteString("  ↑/↓: Navigate  Enter: Select  m: Manual port  a: Active forwards\n")
	s.WriteString("  d: Detach (keep forwards)  q: Quit (close forwards)\n")

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
	s.WriteString("  Esc: Cancel and go back  d: Detach  q: Quit\n")

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

	keys := make([]forwardKey, len(m.ports))
	for i, port := range m.ports {
		keys[i] = m.hostKey(port)
	}
	programWidth := m.programWidth(keys)

	for i, key := range keys {
		cursor := " "
		style := lipgloss.NewStyle()
		if m.cursor == i {
			cursor = ">"
			style = style.Foreground(lipgloss.Color("#FF75B7"))
		}

		checkbox, status, _ := m.renderForwardStatus(key)
		s.WriteString(fmt.Sprintf("%s %s %s%s  %s\n", cursor, style.Render(checkbox),
			style.Render(fmt.Sprintf("Port %-5d", key.remotePort)), m.renderProgram(key, programWidth), status))
	}

	s.WriteString("\n")
	if n := m.activeCount(""); n > 0 {
		s.WriteString(activeStyle.Render(fmt.Sprintf("✓ %d port(s) forwarding", n)))
		s.WriteString("\n\n")
	}
	s.WriteString("Controls:\n")
	s.WriteString("  ↑/↓: Navigate  Space/Enter: Toggle forward  m: Add port  Esc: Back\n")
	s.WriteString("  d: Detach (keep forwards)  q: Quit (close forwards)\n")

	return s.String()
}

// maxProgramWidth caps the program column so long names don't push the status off screen
const maxProgramWidth = 28

// programWidth returns the width of the program column for these rows, 0 if none are known
func (m *Model) programWidth(keys []forwardKey) int {
	width := 0
	for _, key := range keys {
		width = max(width, len([]rune(m.programs[key])))
	}
	return min(width, maxProgramWidth)
}

// renderProgram renders the program column for a row, padded to width
func (m *Model) renderProgram(key forwardKey, width int) string {
	if width == 0 {
		return ""
	}
	name := []rune(m.programs[key])
	if len(name) > width {
		name = append(name[:width-1], '…')
	}
	return "  " + dimStyle.Render(fmt.Sprintf("%-*s", width, string(name)))
}

// renderActiveForwards renders the overview of forwards across all hosts
func (m *Model) renderActiveForwards() string {
	var s strings.Builder

	s.WriteString("Active forwards:\n\n")
	if m.message != "" {
		s.WriteString(startingStyle.Render(m.message))
		s.WriteString("\n\n")
	}

	width := 0
	for _, key := range m.overview {
		width = max(width, len(key.host))
	}
	programWidth := m.programWidth(m.overview)

	for i, key := range m.overview {
		cursor := " "
		style := lipgloss.NewStyle()
		if m.cursor == i {
			cursor = ">"
			style = style.Foreground(lipgloss.Color("#FF75B7"))
		}

		checkbox, status, _ := m.renderForwardStatus(key)
		s.WriteString(fmt.Sprintf("%s %s %s%s  %s\n", cursor, style.Render(checkbox),
			style.Render(fmt.Sprintf("%-*s  Port %-5d", width, key.host, key.remotePort)),
			m.renderProgram(key, programWidth), status))
	}

	s.WriteString("\n")
	s.WriteString("Controls:\n")
	s.WriteString("  ↑/↓: Navigate  Space/Enter: Toggle (close/reopen)  Esc: Host list\n")
	s.WriteString("  d: Detach (keep forwards)  q: Quit (close forwards)\n")

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
	s.WriteString("  Esc: Back  q: Quit (close forwards)\n")

	return s.String()
}
