package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLineTapPassesThroughAndSplitsLines(t *testing.T) {
	var out bytes.Buffer
	var lines []string
	tap := &lineTap{out: &out, onLine: func(l string) { lines = append(lines, l) }}
	// chunks arrive at arbitrary boundaries
	for _, chunk := range []string{"first li", "ne\nsecond line\nthi", "rd line\npartial"} {
		if n, err := tap.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if got := out.String(); got != "first line\nsecond line\nthird line\npartial" {
		t.Fatalf("output was altered: %q", got)
	}
	if strings.Join(lines, "|") != "first line|second line|third line" {
		t.Fatalf("lines: %q", lines)
	}
}

func TestJoinPairing(t *testing.T) {
	j := &joinTracker{byName: map[string]string{}}
	j.mapped("203.0.113.5")
	if ip, amb := j.connected("Alice"); ip != "203.0.113.5" || amb {
		t.Fatalf("single join: %q ambiguous=%v", ip, amb)
	}
	if got := j.disconnected("Alice"); got != "203.0.113.5" {
		t.Fatalf("left ip: %q", got)
	}
	// two players at once: pairing is a guess and must say so
	j.mapped("198.51.100.1")
	j.mapped("198.51.100.2")
	if ip, amb := j.connected("Bob"); ip != "198.51.100.1" || !amb {
		t.Fatalf("simultaneous join: %q ambiguous=%v", ip, amb)
	}
	if ip, amb := j.connected("Carol"); ip != "198.51.100.2" || amb {
		t.Fatalf("second of two: %q ambiguous=%v", ip, amb)
	}
	// a player BDS announces with no mapped connection (e.g. joined before the mux started)
	if ip, _ := j.connected("Ghost"); ip != "" {
		t.Fatalf("unmatched: %q", ip)
	}
}

func TestServerLinesLogNameXuidAndIP(t *testing.T) {
	joins = &joinTracker{byName: map[string]string{}} // isolate from other tests
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	joins.mapped("203.0.113.7")
	handleServerLine(log, "NO LOG FILE! - [2026-10-30 21:08:41:451 INFO] Player connected: Garryx1811, xuid: 2535424608790931")
	handleServerLine(log, "[2026-10-30 21:09:07:291 INFO] Player disconnected: Garryx1811, xuid: 2535424608790931, pfid: E116")
	handleServerLine(log, "[2026-10-30 21:09:08:000 INFO] unrelated console line")
	out := buf.String()
	for _, want := range []string{`msg="player joined"`, "player=Garryx1811", "xuid=2535424608790931", "ip=203.0.113.7", `msg="player left"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("an unrelated line must not log:\n%s", out)
	}
}

func TestGuessFlagOnlyWhenAmbiguous(t *testing.T) {
	joins = &joinTracker{byName: map[string]string{}}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	joins.mapped("203.0.113.1")
	handleServerLine(log, "Player connected: Solo, xuid: 1")
	if strings.Contains(buf.String(), "ip_is_a_guess") {
		t.Fatalf("a single join must not mention the guess flag:\n%s", buf.String())
	}

	buf.Reset()
	joins.mapped("203.0.113.2")
	joins.mapped("203.0.113.3")
	handleServerLine(log, "Player connected: First, xuid: 2")
	if !strings.Contains(buf.String(), "ip_is_a_guess=true") {
		t.Fatalf("simultaneous joins must flag the guess:\n%s", buf.String())
	}
}
