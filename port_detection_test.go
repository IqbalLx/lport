package main

import (
	"reflect"
	"testing"
)

func TestParseListeners(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		ports    []int
		programs map[int]string
	}{
		{
			name: "ss",
			output: `#ss
State  Recv-Q Send-Q    Local Address:Port  Peer Address:PortProcess
LISTEN 0      511           127.0.0.1:6379       0.0.0.0:*
LISTEN 0      4096          127.0.0.1:8000       0.0.0.0:*    users:(("api",pid=2035847,fd=7))
LISTEN 0      4096      127.0.0.53%lo:53         0.0.0.0:*
LISTEN 0      128             0.0.0.0:3000       0.0.0.0:*    users:(("node",pid=10,fd=3))
LISTEN 0      128                [::]:3000          [::]:*    users:(("node",pid=10,fd=4))
LISTEN 0      4096  [fd7a:115c::f73e]:55796         [::]:*
LISTEN 0      5                     *:9000             *:*    users:(("nginx",pid=1,fd=6),("nginx",pid=2,fd=6))
`,
			ports:    []int{53, 3000, 6379, 8000, 9000, 55796},
			programs: map[int]string{3000: "node", 8000: "api", 9000: "nginx"},
		},
		{
			name: "netstat after ss is missing",
			output: `#ss
#netstat
Active Internet connections (only servers)
Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name
tcp        0      0 127.0.0.1:6379          0.0.0.0:*               LISTEN      -
tcp        0      0 127.0.0.1:8000          0.0.0.0:*               LISTEN      2035847/api
tcp6       0      0 :::8000                 :::*                    LISTEN      2035848/gunicorn
udp        0      0 0.0.0.0:68              0.0.0.0:*                           -
`,
			ports:    []int{6379, 8000},
			programs: map[int]string{8000: "api,gunicorn"},
		},
		{
			name: "lsof",
			output: `#ss
#netstat
#lsof
COMMAND     PID USER   FD   TYPE  DEVICE SIZE/OFF NODE NAME
postgres    501 me     7u  IPv6 0x1      0t0  TCP [::1]:5432 (LISTEN)
postgres    501 me     8u  IPv4 0x2      0t0  TCP 127.0.0.1:5432 (LISTEN)
my\x20app   777 me     3u  IPv4 0x3      0t0  TCP *:8080 (LISTEN)
`,
			ports:    []int{5432, 8080},
			programs: map[int]string{5432: "postgres", 8080: "my app"},
		},
		{
			name: "docker containers name their published ports",
			output: `#ss
LISTEN 0      4096          0.0.0.0:5432       0.0.0.0:*
LISTEN 0      4096             [::]:5432          [::]:*
LISTEN 0      4096          0.0.0.0:80         0.0.0.0:*    users:(("docker-proxy",pid=9,fd=4))
LISTEN 0      4096        127.0.0.1:8000       0.0.0.0:*    users:(("api",pid=2,fd=7))
#docker
imigrasi_lgtm|3200/tcp, 0.0.0.0:4317-4318->4317-4318/tcp, [::]:4317-4318->4317-4318/tcp, 0.0.0.0:3300->3000/tcp
imigrasi_pgsql|0.0.0.0:5432->5432/tcp, [::]:5432->5432/tcp
dashboard|0.0.0.0:80->8501/tcp
dns|0.0.0.0:5353->53/udp
idle|
#podman
`,
			// 3300/4317/4318 are published (DNAT) without a host listener; 3200 and udp are not reachable
			ports: []int{80, 3300, 4317, 4318, 5432, 8000},
			programs: map[int]string{
				80: "docker:dashboard", 3300: "docker:imigrasi_lgtm", 4317: "docker:imigrasi_lgtm",
				4318: "docker:imigrasi_lgtm", 5432: "docker:imigrasi_pgsql", 8000: "api",
			},
		},
		{
			name:     "nothing available",
			output:   "#ss\n#netstat\n#lsof\n",
			ports:    []int{},
			programs: map[int]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ports, programs := parseListeners(tt.output)
			if !reflect.DeepEqual(ports, tt.ports) {
				t.Errorf("ports = %v, want %v", ports, tt.ports)
			}
			if !reflect.DeepEqual(programs, tt.programs) {
				t.Errorf("programs = %v, want %v", programs, tt.programs)
			}
		})
	}
}

func TestProgramColumn(t *testing.T) {
	m := newTestModel("h")
	m.state = StateConnecting
	m.Update(PortsDetectedMsg{Ports: []int{22, 8000}, Programs: map[int]string{8000: "a-very-long-program-name-indeed"}})

	width := m.programWidth([]forwardKey{m.hostKey(22), m.hostKey(8000)})
	if width != maxProgramWidth {
		t.Fatalf("width = %d, want capped at %d", width, maxProgramWidth)
	}
	if got := []rune(m.renderProgram(m.hostKey(22), width)); len(got) < width {
		t.Errorf("unknown program not padded: %q", string(got))
	}

	// Re-detecting replaces the host's programs
	m.state = StateConnecting
	m.Update(PortsDetectedMsg{Ports: []int{22}, Programs: map[int]string{22: "sshd"}})
	if m.programs[m.hostKey(8000)] != "" || m.programs[m.hostKey(22)] != "sshd" {
		t.Errorf("programs not refreshed: %v", m.programs)
	}
}
