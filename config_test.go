package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigExpandsEnvPlaceholders(t *testing.T) {
	t.Setenv("EXECUTABLE", "./bedrock_server")
	t.Setenv("BACKEND_PORT", "19140")
	t.Setenv("SERVERNAME", `My "Quoted" Server`)
	t.Setenv("MAX_PLAYERS", "12")
	cfg, err := loadConfig(writeConfig(t, `{
		"server_command": ["{executable}"],
		"backend_port": "{backend_port}",
		"properties": {"server-port": "{port}"},
		"motd": "{servername}",
		"max_players": "{max_players}"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerCommand[0] != "./bedrock_server" || cfg.BackendPort != 19140 || cfg.MaxPlayers != 12 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Motd != `My "Quoted" Server` {
		t.Fatalf("motd = %q", cfg.Motd)
	}
	if cfg.Properties["server-port"] != "{port}" {
		t.Fatalf("{port} must be left for the launcher, got %q", cfg.Properties["server-port"])
	}
}

func TestLoadConfigDefaultsAndMissingVariables(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `{"max_players": "{max_players:7}", "motd": "{servername:Fallback}"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxPlayers != 7 || cfg.Motd != "Fallback" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}

	_, err = loadConfig(writeConfig(t, `{"backend_port": "{backend_port}"}`))
	if err == nil || !strings.Contains(err.Error(), "BACKEND_PORT") {
		t.Fatalf("want an error naming BACKEND_PORT, got %v", err)
	}
}

func TestFlexInt(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `{"backend_port": "19140", "max_players": 10}`))
	if err != nil || cfg.BackendPort != 19140 || cfg.MaxPlayers != 10 {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if _, err = loadConfig(writeConfig(t, `{"backend_port": "abc"}`)); err == nil {
		t.Fatal("want an error for a non-numeric port")
	}
}

func TestMuxExampleConfigLoads(t *testing.T) {
	t.Setenv("EXECUTABLE", "./bedrock_server")
	t.Setenv("BACKEND_PORT", "19140")
	t.Setenv("MAX_PLAYERS", "25")
	cfg, err := loadConfig("config.mux.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicTransport != "mux" || cfg.BackendPort != 19140 || cfg.ServerCommand[0] != "./bedrock_server" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	p := cfg.Properties
	if p["max-players"] != "25" || p["allow-list"] != "false" || p["online-mode"] != "true" ||
		p["server-udp-ports"] != "19141-19240" || p["server-port"] != "{port}" {
		t.Fatalf("unexpected properties: %v", p)
	}
}

func TestEnvModeBuildsTheBedrockSetup(t *testing.T) {
	t.Setenv("PROXY_COMMAND", "./bedrock_server")
	t.Setenv("BACKEND_PORT", "")
	t.Setenv("UDP_PORTS", "")
	t.Setenv("ALLOW_LIST", "")
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.fromEnv || cfg.PublicTransport != "mux" || cfg.BackendPort != 19140 || cfg.ServerDir != "." ||
		len(cfg.ServerCommand) != 1 || cfg.ServerCommand[0] != "./bedrock_server" {
		t.Fatalf("unexpected env config: %+v", cfg)
	}
	p := cfg.Properties
	if p["server-port"] != "{port}" || p["online-mode"] != "true" || p["transport"] != "nethernet" ||
		p["server-udp-ports"] != "19141-19240" {
		t.Fatalf("unexpected properties: %v", p)
	}
	if _, ok := p["allow-list"]; ok {
		t.Fatal("allow-list must be left alone when ALLOW_LIST is unset")
	}
}

func TestEnvModeOverridesAndSplitsTheCommand(t *testing.T) {
	t.Setenv("PROXY_COMMAND", "box64 ./bedrock_server")
	t.Setenv("BACKEND_PORT", "20000")
	t.Setenv("UDP_PORTS", "30000-30050")
	t.Setenv("ALLOW_LIST", "false")
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.ServerCommand, "|") != "box64|./bedrock_server" || cfg.BackendPort != 20000 ||
		cfg.Properties["server-udp-ports"] != "30000-30050" || cfg.Properties["allow-list"] != "false" {
		t.Fatalf("unexpected env config: %+v", cfg)
	}
}

func TestEnvModeIgnoresConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"public_transport": "raknet", "server_command": ["/bin/sh", "-c", "evil"], "backend_port": 1}`), 0o644)
	t.Setenv("PROXY_COMMAND", "./bedrock_server")
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicTransport != "mux" || cfg.ServerCommand[0] != "./bedrock_server" || cfg.BackendPort != 19140 {
		t.Fatalf("a config.json must be ignored in managed mode: %+v", cfg)
	}
}

func TestEnvModeRejectsValuesThatCouldInjectProperties(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"UDP_PORTS", "19141-19240\nonline-mode=false"},
		{"UDP_PORTS", "abc"},
		{"ALLOW_LIST", "yes"},
		{"ALLOW_LIST", "false\nonline-mode=false"},
		{"BACKEND_PORT", "0"},
		{"BACKEND_PORT", "70000"},
		{"BACKEND_PORT", "19140\nx=y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROXY_COMMAND", "./bedrock_server")
			t.Setenv(tc.name, tc.value)
			if _, err := loadConfig(filepath.Join(t.TempDir(), "config.json")); err == nil {
				t.Fatalf("%s=%q must be rejected", tc.name, tc.value)
			}
		})
	}
}

func TestNoEnvModeKeepsTheOldBehaviour(t *testing.T) {
	t.Setenv("PROXY_COMMAND", "")
	dir := t.TempDir()
	cfg, err := loadConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.fromEnv {
		t.Fatal("without PROXY_COMMAND the environment must not configure the proxy")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal("without PROXY_COMMAND a default config.json must still be written")
	}
}
