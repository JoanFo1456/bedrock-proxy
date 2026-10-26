package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const sampleOffer = "v=0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\n" +
	"a=candidate:candidate:1 1 udp 2130706431 172.18.0.46 5000 typ host generation 0\r\n" +
	"a=candidate:candidate:2 1 udp 2130706431 fdba:17c8:6c94::2d 5001 typ host generation 0\r\n" +
	"a=ice-ufrag:abcd\r\n"

func TestOfferTransformations(t *testing.T) {
	if got := fixPrefix(sampleOffer); strings.Contains(got, "candidate:candidate:") {
		t.Fatalf("prefix not fixed: %q", got)
	}
	v4 := ipv4Only(sampleOffer)
	if strings.Contains(v4, "fdba") || !strings.Contains(v4, "172.18.0.46") || !strings.Contains(v4, "a=ice-ufrag:abcd") {
		t.Fatalf("ipv4Only wrong: %q", v4)
	}
	if got := noCandidates(sampleOffer); strings.Contains(got, "a=candidate:") || !strings.Contains(got, "a=ice-ufrag:abcd") {
		t.Fatalf("noCandidates wrong: %q", got)
	}
}

func TestProbeSendsAllVariants(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Write([]byte("v=0\r\nm=application 19140 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 172.17.0.2\r\na=candidate:9 1 udp 1 172.17.0.2 19140 typ host\r\n"))
	}))
	defer srv.Close()

	recordOffer(srv.URL+"/v1/join/123", sampleOffer)
	var logs bytes.Buffer
	probeOfferVariants(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	if len(bodies) != 5 {
		t.Fatalf("want 5 probe requests, got %d", len(bodies))
	}
	if !strings.Contains(logs.String(), "answer_candidates=1") || !strings.Contains(logs.String(), `variant=no-candidates`) {
		t.Fatalf("unexpected log output:\n%s", logs.String())
	}
}
