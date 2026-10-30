package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// freePort asks the OS for an unused loopback UDP port.
func freePort() (int, error) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port, nil
}

// applyProperties forces key=value pairs into a properties file, keeping every other line untouched.
func applyProperties(path string, props map[string]string) error {
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	done := map[string]bool{}
	for i, l := range lines {
		k, _, ok := strings.Cut(l, "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		if v, ok := props[strings.TrimSpace(k)]; ok {
			lines[i] = strings.TrimSpace(k) + "=" + v
			done[strings.TrimSpace(k)] = true
		}
	}
	for k, v := range props {
		if !done[k] {
			lines = append(lines, k+"="+v)
		}
	}
	out := []byte(strings.Join(lines, "\n") + "\n")
	if err := os.WriteFile(path, out, 0o644); err == nil {
		return nil
	}
	// A read-only file in a writable folder can still be replaced.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runServer starts the configured server and blocks until it exits. When ctx is cancelled it asks the
// server to stop through its stdin, then kills it once the stop timeout passes. Console input to the
// proxy is forwarded to the server, and its output is forwarded back, so the panel console keeps working.
func runServer(ctx context.Context, cfg Config, backendPort int, log *slog.Logger) error {
	port := fmt.Sprint(backendPort)
	repl := func(s string) string { return strings.ReplaceAll(s, "{port}", port) }

	dir, err := filepath.Abs(cfg.ServerDir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("server_dir %q is not a directory", dir)
	}

	if len(cfg.Properties) > 0 {
		props := make(map[string]string, len(cfg.Properties))
		for k, v := range cfg.Properties {
			props[k] = repl(v)
		}
		file := cfg.PropertiesFile
		if file == "" {
			file = "server.properties"
		}
		// Not fatal: on some hosts the file is not writable by the server user. The values must then be
		// set by hand, and backend_port must be fixed so they stay valid across restarts.
		if err := applyProperties(filepath.Join(dir, file), props); err != nil {
			log.Warn("could not update properties file; set these values yourself", "file", file, "props", props, "err", err)
		}
	}

	// A command that exists in the server folder wins over PATH, so "bedrock_server" just works.
	name := repl(cfg.ServerCommand[0])
	if !filepath.IsAbs(name) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil || strings.ContainsAny(name, `/\`) {
			name = filepath.Join(dir, name)
		}
	}
	args := make([]string, 0, len(cfg.ServerCommand)-1)
	for _, a := range cfg.ServerCommand[1:] {
		args = append(args, repl(a))
	}

	// Plain exec.Command, not CommandContext: cancelling must stop the server gracefully, not kill it.
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BACKEND_PORT="+port)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if cfg.PublicTransport == transportMux {
		tap := func(w io.Writer) io.Writer {
			return &lineTap{out: w, onLine: func(l string) { handleServerLine(log, l) }}
		}
		cmd.Stdout, cmd.Stderr = tap(os.Stdout), tap(os.Stderr)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	log.Info("server started", "pid", cmd.Process.Pid, "dir", dir, "backend_port", backendPort)

	go func() { // panel console -> server stdin
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if _, err := io.WriteString(stdin, sc.Text()+"\n"); err != nil {
				return
			}
		}
	}()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case err := <-exited:
		return err
	case <-ctx.Done():
	}

	log.Info("stopping server", "command", cfg.StopCommand)
	_, _ = io.WriteString(stdin, cfg.StopCommand+"\n")
	timeout := time.Duration(cfg.StopTimeoutSeconds) * time.Second
	select {
	case err := <-exited:
		return err
	case <-time.After(timeout):
		log.Warn("server did not stop in time, killing it", "timeout", timeout)
		_ = cmd.Process.Kill()
		return <-exited
	}
}
