package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// logNetDiagnostics records the container's addresses and open sockets. It runs only in debug mode, after
// a failed backend dial, to show whether the server actually opened the sockets NetherNet needs.
func logNetDiagnostics(log *slog.Logger) {
	var addrs []string
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifc := range ifaces {
			a, _ := ifc.Addrs()
			for _, x := range a {
				addrs = append(addrs, ifc.Name+"="+x.String())
			}
		}
	}
	log.Debug("diagnostics: interface addresses", "addrs", strings.Join(addrs, " "))

	for _, f := range []struct{ file, kind string }{
		{"/proc/net/udp", "udp4"}, {"/proc/net/udp6", "udp6"},
		{"/proc/net/tcp", "tcp4"}, {"/proc/net/tcp6", "tcp6"},
	} {
		b, err := os.ReadFile(f.file)
		if err != nil {
			continue // not Linux, or not readable
		}
		var socks []string
		for i, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if i == 0 || len(fields) < 4 {
				continue
			}
			ip, port, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			p, _ := strconv.ParseUint(port, 16, 16)
			state := ""
			if strings.HasPrefix(f.kind, "tcp") {
				if fields[3] != "0A" { // listening sockets only
					continue
				}
				state = " listen"
			}
			socks = append(socks, fmt.Sprintf("%s:%d%s", ip, p, state))
		}
		log.Debug("diagnostics: open sockets", "kind", f.kind, "sockets", strings.Join(socks, " "))
	}
}

// logHostDiagnostics records facts about the machine and container sandbox. A server that cannot open the
// UDP sockets NetherNet needs is usually limited by the host (kernel, CPU emulation, a sandboxed runtime).
func logHostDiagnostics(log *slog.Logger) {
	log.Debug("diagnostics: host", "goos", runtime.GOOS, "goarch", runtime.GOARCH, "cpus", runtime.NumCPU())
	one := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return "unreadable"
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}
	grep := func(path string, prefixes ...string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return "unreadable"
		}
		var out []string
		seen := map[string]bool{}
		for _, line := range strings.Split(string(b), "\n") {
			for _, p := range prefixes {
				if strings.HasPrefix(line, p) && !seen[p] {
					seen[p] = true
					out = append(out, strings.Join(strings.Fields(line), " "))
					break
				}
			}
		}
		return strings.Join(out, " | ")
	}
	_, dockerenv := os.Stat("/.dockerenv")
	log.Debug("diagnostics: kernel",
		"version", one("/proc/version"),
		"cpu", grep("/proc/cpuinfo", "model name", "Hardware", "Model", "CPU implementer", "vendor_id"),
		"sandbox", grep("/proc/self/status", "Seccomp", "NoNewPrivs", "CapEff", "CapBnd"),
		"dockerenv", dockerenv == nil,
		"cgroup", one("/proc/1/cgroup"))
	log.Debug("diagnostics: network sysctls",
		"ip_local_port_range", one("/proc/sys/net/ipv4/ip_local_port_range"),
		"disable_ipv6", one("/proc/sys/net/ipv6/conf/all/disable_ipv6"),
		"somaxconn", one("/proc/sys/net/core/somaxconn"))
}
