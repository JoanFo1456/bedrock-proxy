package main

import (
	"io"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// In mux mode the proxy knows each player's real address but not who they are (they talk to BDS end to end),
// while BDS knows the name but not the address. A player's first UDP packet and BDS's "Player connected" line
// are about 100 ms apart, so the two can be paired to log name, XUID and address together.

const joinPairWindow = 30 * time.Second

type joinEntry struct {
	ip string
	at time.Time
}

type joinTracker struct {
	mu      sync.Mutex
	pending []joinEntry       // mapped connections BDS has not yet announced
	byName  map[string]string // name -> ip, for the matching "left" line
}

var joins = &joinTracker{byName: map[string]string{}}

// mapped records a connection the mux just attached to a BDS port.
func (j *joinTracker) mapped(ip string) {
	j.mu.Lock()
	j.pending = append(j.pending, joinEntry{ip: ip, at: time.Now()})
	j.mu.Unlock()
}

// connected pairs BDS's announcement with the oldest unmatched connection. ambiguous reports that several
// players joined at the same moment, so the pairing is a best guess.
func (j *joinTracker) connected(name string) (ip string, ambiguous bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cutoff := time.Now().Add(-joinPairWindow)
	live := j.pending[:0]
	for _, e := range j.pending {
		if e.at.After(cutoff) {
			live = append(live, e)
		}
	}
	j.pending = live
	if len(j.pending) == 0 {
		return "", false
	}
	ip, ambiguous = j.pending[0].ip, len(j.pending) > 1
	j.pending = j.pending[1:]
	j.byName[name] = ip
	return ip, ambiguous
}

func (j *joinTracker) disconnected(name string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	ip := j.byName[name]
	delete(j.byName, name)
	return ip
}

var (
	reConnected    = regexp.MustCompile(`Player connected: ([^,]+), xuid: (\d*)`)
	reDisconnected = regexp.MustCompile(`Player disconnected: ([^,]+), xuid: (\d*)`)
)

// handleServerLine reacts to BDS console lines about players.
func handleServerLine(log *slog.Logger, line string) {
	if m := reConnected.FindStringSubmatch(line); m != nil {
		ip, ambiguous := joins.connected(m[1])
		if ip == "" {
			ip = "unknown"
		}
		attrs := []any{"player", m[1], "xuid", m[2], "ip", ip}
		if ambiguous { // only worth mentioning when several players joined at the same moment
			attrs = append(attrs, "ip_is_a_guess", true)
		}
		log.Info("player joined", attrs...)
	} else if m := reDisconnected.FindStringSubmatch(line); m != nil {
		log.Info("player left", "player", m[1], "xuid", m[2], "ip", joins.disconnected(m[1]))
	}
}

// lineTap passes everything through to out unchanged and reports each complete line.
type lineTap struct {
	out    io.Writer
	onLine func(string)
	buf    []byte
}

func (t *lineTap) Write(p []byte) (int, error) {
	n, err := t.out.Write(p)
	t.buf = append(t.buf, p...)
	for {
		i := -1
		for k, c := range t.buf {
			if c == '\n' {
				i = k
				break
			}
		}
		if i < 0 {
			break
		}
		if t.onLine != nil {
			t.onLine(string(t.buf[:i]))
		}
		t.buf = t.buf[i+1:]
	}
	if len(t.buf) > 64*1024 { // never grow without bound on a stream with no newlines
		t.buf = t.buf[:0]
	}
	return n, err
}
