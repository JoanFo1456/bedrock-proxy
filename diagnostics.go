package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
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
