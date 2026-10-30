package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Config is read from config.json next to the executable. A default one is written on first run.
type Config struct {
	// Host and Port are where players connect: TCP for NetherNet signaling and UDP for WebRTC, both on Port.
	// A Port of 0 uses the SERVER_PORT environment variable (set by Pterodactyl), then 19132.
	Host string  `json:"host"`
	Port flexInt `json:"port"`

	// ServerDir is the folder the managed server runs in. ServerCommand is the executable and arguments to
	// start there. "{port}" in any argument is replaced with the internal backend port. An empty
	// ServerCommand makes the proxy run on its own against BackendAddress.
	ServerDir     string   `json:"server_dir"`
	ServerCommand []string `json:"server_command"`
	// StopCommand is written to the server's stdin to shut it down gracefully ("stop" for most servers).
	StopCommand string `json:"stop_command"`
	// StopTimeoutSeconds is how long the server gets to exit after StopCommand before it is killed.
	StopTimeoutSeconds flexInt `json:"stop_timeout_seconds"`

	// PublicTransport is what players connect with: "raknet" (default, one UDP port), "nethernet"
	// (TCP signaling + WebRTC UDP on the same port number) or "mux" (players talk to the server end to end;
	// the proxy only forwards their packets through one port, see mux.go).
	PublicTransport string `json:"public_transport"`
	// PublicIP is the address players send UDP to in "mux" mode. Empty uses the host they connected to.
	PublicIP string `json:"public_ip"`
	// BackendTransport is how the proxy talks to the server: "nethernet" (default, what current BDS
	// needs; the server must run with online-mode=false) or "raknet" (PocketMine, Dragonfly, ...).
	BackendTransport string `json:"backend_transport"`

	// BackendPort is the port the server listens on. 0 picks a free one, which only works when the
	// proxy can rewrite the server's properties; otherwise set it and write the same value there.
	BackendPort flexInt `json:"backend_port"`
	// BackendAddress overrides the backend address, for servers not launched by the proxy. For a
	// NetherNet backend it is a URL such as "http://127.0.0.1:19140".
	BackendAddress string `json:"backend_address"`

	// Properties are key=value lines forced into PropertiesFile (relative to ServerDir) before launch,
	// e.g. server-port. "{port}" is replaced with the backend port.
	PropertiesFile string            `json:"properties_file"`
	Properties     map[string]string `json:"properties"`

	// Motd and MaxPlayers are what the server list shows. The proxy does not enforce MaxPlayers on the
	// backend, which has its own limit.
	Motd       string  `json:"motd"`
	MaxPlayers flexInt `json:"max_players"`
	// IdentityKeyFile stores the signaling identity key. Keeping it stable across restarts avoids the
	// "trust this server?" prompt players get when the identity changes.
	IdentityKeyFile string `json:"identity_key_file"`

	// ICEServers are STUN/TURN URLs offered to clients, needed when the host is behind NAT.
	ICEServers []string `json:"ice_servers"`
}

func defaultConfig() Config {
	return Config{
		Host:               "0.0.0.0",
		ServerDir:          "server",
		ServerCommand:      nil,
		PublicTransport:    "raknet",
		BackendTransport:   "nethernet",
		Motd:               "Minecraft Server",
		MaxPlayers:         20,
		IdentityKeyFile:    "identity.key",
		StopCommand:        "stop",
		StopTimeoutSeconds: 30,
		ICEServers:         []string{"stun:stun.l.google.com:19302"},
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		out, _ := json.MarshalIndent(cfg, "", "  ")
		if werr := os.WriteFile(path, append(out, '\n'), 0o644); werr != nil {
			return cfg, fmt.Errorf("write default config: %w", werr)
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	// Decode generically first so {placeholders} are expanded inside string values only, never in JSON syntax.
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	raw, err = expandAll(raw)
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	expanded, err := json.Marshal(raw)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(expanded, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// publicPort returns the port players connect to.
func (c Config) publicPort() int {
	if c.Port != 0 {
		return int(c.Port)
	}
	if v, err := strconv.Atoi(os.Getenv("SERVER_PORT")); err == nil && v > 0 {
		return v
	}
	return 19132
}
