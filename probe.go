package main

import (
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lastOffer remembers the most recent offer sent to the backend so a failed join can be re-tried in
// variants (debug mode only).
var lastOffer struct {
	sync.Mutex
	url, body string
}

func recordOffer(url, body string) {
	lastOffer.Lock()
	lastOffer.url, lastOffer.body = url, body
	lastOffer.Unlock()
}

func sdpLines(sdp string, keep func(line string) bool) string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(sdp, "\r\n"), "\r\n") {
		if keep(l) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\r\n") + "\r\n"
}

func isCandidate(l string) bool { return strings.HasPrefix(l, "a=candidate:") }

// candidateAddr returns the address field of an "a=candidate:" line.
func candidateAddr(l string) string {
	f := strings.Fields(l)
	if len(f) > 4 {
		return f[4]
	}
	return ""
}

func fixPrefix(sdp string) string {
	return strings.ReplaceAll(sdp, "a=candidate:candidate:", "a=candidate:")
}

func ipv4Only(sdp string) string {
	return sdpLines(sdp, func(l string) bool { return !isCandidate(l) || !strings.Contains(candidateAddr(l), ":") })
}

func noCandidates(sdp string) string {
	return sdpLines(sdp, func(l string) bool { return !isCandidate(l) })
}

func firstLine(sdp, prefix string) string {
	for _, l := range strings.Split(sdp, "\r\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// probeOfferVariants re-sends the last offer to the backend in several forms and logs whether the answer
// contains ICE candidates. It changes nothing; it only reports which offer shape the backend accepts.
func probeOfferVariants(log *slog.Logger) {
	lastOffer.Lock()
	url, body := lastOffer.url, lastOffer.body
	lastOffer.Unlock()
	i := strings.LastIndex(url, "/")
	if body == "" || i < 0 {
		return
	}
	base := url[:i+1]
	variants := []struct {
		name string
		sdp  string
	}{
		{"as-is", body},
		{"fix-candidate-prefix", fixPrefix(body)},
		{"ipv4-only", ipv4Only(body)},
		{"fix-prefix+ipv4-only", ipv4Only(fixPrefix(body))},
		{"no-candidates", noCandidates(body)},
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, v := range variants {
		req, err := http.NewRequest(http.MethodPost, base+strconv.FormatUint(rand.Uint64(), 10), strings.NewReader(v.sdp))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/sdp")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			log.Debug("probe: offer variant", "variant", v.name, "err", err)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ans := string(b)
		log.Debug("probe: offer variant", "variant", v.name, "status", resp.StatusCode,
			"took", time.Since(start).Round(time.Millisecond), "answer_candidates", strings.Count(ans, "a=candidate:"),
			"m_line", firstLine(ans, "m="), "c_line", firstLine(ans, "c="))
	}
}
