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
