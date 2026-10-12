# lport - SSH Port Forwarder TUI

A terminal user interface (TUI) application for SSH port forwarding that reads from your local SSH config, allows you to select SSH connections, detects running ports on remote hosts, and forwards them to localhost. You can forward as many ports as you like at the same time.

> lport is a fork of [kport](https://code.nym.sh/kennethnym/kport) by kennethnym (k → l), adding multi-port forwarding.

![lport TUI Interface](screenshots/main.png)

*Interactive terminal interface for SSH port forwarding with host selection and port detection*

## Features

- **SSH Config Integration**: Automatically reads from `~/.ssh/config`
- **Include Support**: Supports SSH config `Include` directive with glob patterns
- **Full SSH Compatibility**: Uses native `ssh` command - supports ProxyCommand, jump hosts, and all SSH features
- **Interactive Host Selection**: Choose from configured SSH hosts using arrow keys
- **Automatic Port Detection**: Scans remote host for listening ports using `netstat`, `ss`, or `lsof`
- **Multi-Port Forwarding**: Toggle any number of ports with checkboxes; each shows a live status indicator
- **Manual Port Forwarding**: Add remote ports that weren't detected
- **Smart Port Mapping**: Tries to use same port locally (e.g., remote:3000 → localhost:3000)
- **Detach**: Quit with `d` to leave tunnels running; the next `lport` finds them again so you can keep or close them
- **Real-time Port Forwarding**: Creates SSH tunnels using `ssh -L` command
- **Clean TUI Interface**: Built with Bubble Tea for a smooth terminal experience

## Installation

On macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/IqbalLx/lport/main/install.sh | sh
```

Then open a new terminal and run `lport`. Running the same command again updates it.

The installer checks the download against the release checksums, installs into `~/.lport/bin` without `sudo`, and adds that folder to PATH in your shell's startup file (one marked block). Overrides:

- `LPORT_VERSION=v0.1.0` installs a specific release
- `LPORT_HOME=...` installs somewhere other than `~/.lport`
- `LPORT_NO_MODIFY_PATH=1` leaves your shell startup file alone

With Go 1.26+ you can also run `go install github.com/IqbalLx/lport@latest`, or `go build -o lport` from a clone.

## Usage

1. **Run the application**:
   ```bash
   lport
   ```

2. **Test port mapping** (optional):
   ```bash
   lport --test-port 3000
   # Shows: ✅ Port 3000 available locally - using same port
   #        Mapping: localhost:3000 -> remote:3000
   ```

3. **Select SSH Host**: Use arrow keys to navigate and press Enter to select an SSH host from your config

4. **Choose Ports**: 
   - The app will automatically detect open ports on the remote host
   - Press Space or Enter on a port to start forwarding it; press again to stop
   - Select as many ports as you like - each runs its own tunnel
   - Press 'm' to add a port manually

   ```
   Ports on my-server:

     [ ] Port 22
     [x] Port 3000   ◌ connecting...
     [x] Port 5432   ● localhost:5432
   > [x] Port 8080   ● localhost:54012 (port 8080 was in use)
     [ ] Port 9000   ✗ connect to host my-server port 22: Connection refused

   ✓ 2 port(s) forwarding
   ```

   - `◌ connecting...` - the tunnel is being set up
   - `● localhost:N` - the tunnel is accepting connections on localhost
   - `✗ reason` - the tunnel failed or dropped; toggle again to retry

5. **Port Forwarding**: 
   - lport tries to use the same port locally (e.g., remote:3000 → localhost:3000)
   - If unavailable, it uses a random available port
   - Clear feedback shows the actual mapping and access URLs

6. **Leaving**:
   - `q` closes every tunnel and quits
   - `d` detaches: lport quits but the tunnels keep running, even after you close the terminal. It prints what is still open:
     ```
     Detached. 2 forward(s) still running:
       my-server:5432 -> localhost:5432
       my-server:8080 -> localhost:54012
     Run lport again to manage them.
     ```
   - The next time you start lport it finds those tunnels and opens the **Active forwards** screen, where you can close them (Space/Enter) or keep them (Esc). Tunnels left by kport are recognized too.

## Controls

### Host Selection
- `↑/↓` or `j/k`: Navigate through SSH hosts
- `Enter`: Select host and detect ports
- `m`: Manual port forwarding for selected host
- `a`: Active forwards across all hosts
- `d`: Detach (quit, keep tunnels running)
- `q`: Quit and close all tunnels

### Port Selection
- `↑/↓` or `j/k`: Navigate through detected ports
- `Space` / `Enter`: Toggle forwarding for the selected port
- `m`: Add a port manually
- `Esc`: Back to host selection (tunnels keep running)
- `d`: Detach (quit, keep tunnels running)
- `q`: Quit and close all tunnels

### Active Forwards
- `↑/↓` or `j/k`: Navigate
- `Space` / `Enter`: Close a tunnel, or reopen it
- `Esc`: Back to host selection
- `d` / `q`: Detach / quit and close all tunnels

### Manual Port Entry
- `0-9`: Enter port number
- `Backspace`: Delete last digit
- `Enter`: Add the port to the list and start forwarding it
- `Esc`: Go back to previous screen
- `q`: Quit application

## SSH Configuration

The application reads from your standard SSH config file at `~/.ssh/config`. Example configuration:

```
Host my-server
    HostName example.com
    User myuser
    Port 22
    IdentityFile ~/.ssh/id_rsa

Host dev-box
    HostName dev.example.com
    User developer
    Port 2222

# Include additional config files
Include ~/.ssh/config.d/*
Include ~/.ssh/work-config
```

### Include Support

lport supports the SSH `Include` directive, allowing you to organize your SSH configuration across multiple files:

- **Glob patterns**: `Include ~/.ssh/config.d/*`
- **Specific files**: `Include ~/.ssh/work-config`
- **Relative paths**: `Include config.d/servers`
- **Quoted paths**: `Include "gitpod/config"` or `Include 'path with spaces/config'`
- **Cycle detection**: Prevents infinite loops from circular includes

Relative paths in includes are resolved relative to `~/.ssh/` directory, matching OpenSSH behavior.

## Authentication

The application uses the native `ssh` command, so it supports all SSH authentication methods:
- SSH key-based authentication (using IdentityFile from config)
- SSH agent authentication (if SSH_AUTH_SOCK is set)
- ProxyCommand for jump hosts and SSH containers
- All other SSH configuration options (ControlMaster, etc.)

This means if you can connect with `ssh hostname`, lport will work too!

## Requirements

- Go 1.26 or later
- SSH access to remote hosts
- SSH config file at `~/.ssh/config`
- Key or agent based authentication (ssh runs with `BatchMode=yes`, so password prompts are not supported)

## Dependencies

- `charm.land/bubbletea/v2` - TUI framework
- `charm.land/lipgloss/v2` - Terminal styling

## How It Works

1. **Config Parsing**: Reads and parses your SSH config file to extract host information
2. **SSH Connection**: Uses native `ssh` command with all your configured options
3. **Port Detection**: Runs commands like `netstat -tlnp` on the remote host via SSH to find listening ports
4. **Port Forwarding**: Uses `ssh -L localport:localhost:remoteport hostname` for tunneling, one process per port. A port is marked active once its local end accepts connections
5. **Full Compatibility**: Works with ProxyCommand, jump hosts, SSH containers, and all SSH features

## Expected Behavior

When you select an SSH host:

1. **Connection Success**: Shows detected ports or "No ports detected" with option for manual entry
2. **Connection Failure**: Shows "Could not connect" message with option for manual port forwarding
3. **Timeout**: Connection attempts timeout after 5 seconds to avoid hanging

The application gracefully handles connection failures and allows you to:
- Go back to host selection with `Esc`
- Try manual port forwarding with `m`
- Quit with `q`

## Limitations

- Requires `ssh` command to be available in PATH
- Port detection requires `netstat`, `ss`, or `lsof` on the remote host
- Connection failures are expected for non-existent or unreachable hosts

## Advantages of Using Native SSH Command

- **Full SSH Feature Support**: ProxyCommand, ControlMaster, jump hosts, etc.
- **Consistent Behavior**: Same authentication and connection logic as your terminal
- **SSH Container Support**: Works with containers that require ProxyCommand
- **No Additional Setup**: If `ssh hostname` works, lport works too

## Development

```sh
go test -race ./...   # forwarding tests use testdata/fake-ssh (needs python3), no SSH server required
go build -o lport
```

CI runs formatting, vet and the tests on Linux and macOS, shellchecks `install.sh`, and installs a locally built release with it.

To release, push a tag:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

The release workflow builds `darwin-arm64`, `darwin-x64`, `linux-x64` and `linux-arm64` on matching runners, smoke-tests each binary, and publishes them with `SHA256SUMS` for `install.sh`.

## License

MIT License