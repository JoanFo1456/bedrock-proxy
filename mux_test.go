package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func stunRequest(username string) []byte {
	attr := append([]byte{0x00, 0x06, 0, byte(len(username))}, username...)
	for len(attr)%4 != 0 {
		attr = append(attr, 0)
	}
	msg := make([]byte, 20, 20+len(attr))
	binary.BigEndian.PutUint16(msg[0:2], 0x0001)
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(attr)))
	binary.BigEndian.PutUint32(msg[4:8], 0x2112A442)
	return append(msg, attr...)
}

func TestStunUsername(t *testing.T) {
	for _, name := range []string{"abcd:efgh", "d/gI:Vn5e", "a:b", "toolongusername12:x"} {
		got, ok := stunUsername(stunRequest(name))
		if !ok || got != name {
			t.Fatalf("username %q: got %q ok=%v", name, got, ok)
		}
	}
	if _, ok := stunUsername([]byte("hello world, definitely not stun")); ok {
		t.Fatal("plain data must not parse as STUN")
	}
	if _, ok := stunUsername(nil); ok {
		t.Fatal("empty packet must not parse")
	}
}

const goodAnswer = "v=0\r\nm=application 19141 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 172.17.0.2\r\n" +
	"a=candidate:3569714587 1 udp 2122260223 172.17.0.2 19141 typ host generation 0 network-id 1\r\n" +
	"a=candidate:99 1 udp 2122260223 fdba:17c8:6c94::2d 19141 typ host generation 0\r\n" +
	"a=ice-ufrag:d/gI\r\na=ice-pwd:secret\r\na=setup:active\r\n"

func TestAnswerHandling(t *testing.T) {
	ufrag, internal, err := parseAnswer(goodAnswer)
	if err != nil || ufrag != "d/gI" || internal != netip.MustParseAddrPort("172.17.0.2:19141") {
		t.Fatalf("parseAnswer: %q %v %v", ufrag, internal, err)
	}
	out := rewriteAnswer(goodAnswer, netip.MustParseAddrPort("203.0.113.9:19105"))
	for _, want := range []string{"m=application 19105 ", "c=IN IP4 203.0.113.9", "a=candidate:1 1 udp 2130706431 203.0.113.9 19105 typ host", "a=ice-ufrag:d/gI", "a=setup:active"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rewritten answer lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "172.17.0.2") || strings.Contains(out, "fdba") {
		t.Fatalf("internal addresses leaked:\n%s", out)
	}
	if _, _, err := parseAnswer("v=0\r\nm=application 9 UDP/DTLS/SCTP x\r\na=ice-ufrag:abcd\r\n"); err == nil {
		t.Fatal("an answer without candidates must be rejected")
	}
	if got := stripCandidates("a=x\r\na=candidate:1 2\r\na=end-of-candidates\r\na=y\r\n"); got != "a=x\r\na=y\r\n" {
		t.Fatalf("stripCandidates: %q", got)
	}
}

// fakeServer is a stand-in for BDS: a signaling endpoint plus one UDP port per player.
type fakeServer struct {
	http    *httptest.Server
	socks   []*net.UDPConn
	mu      sync.Mutex
	offers  []string
	counter int
}

func newFakeServer(t *testing.T, players int) *fakeServer {
	fs := &fakeServer{}
	for i := 0; i < players; i++ {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		fs.socks = append(fs.socks, c)
	}
	fs.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fs.mu.Lock()
		i := fs.counter
		fs.counter++
		fs.offers = append(fs.offers, string(b))
		fs.mu.Unlock()
		port := fs.socks[i].LocalAddr().(*net.UDPAddr).Port
		fmt.Fprintf(w, "v=0\r\nm=application %d UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 127.0.0.1\r\n"+
			"a=candidate:1 1 udp 1 127.0.0.1 %d typ host\r\na=ice-ufrag:srv%d\r\n", port, port, i)
	}))
	t.Cleanup(fs.http.Close)
	return fs
}

func readFrom(t *testing.T, c *net.UDPConn) ([]byte, net.Addr) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, addr, err := c.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf[:n], addr
}

func TestMuxRoutesPlayersToTheirOwnServerPort(t *testing.T) {
	fs := newFakeServer(t, 2)

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	pubPort := pc.LocalAddr().(*net.UDPAddr).Port
	m := newUDPMux(pc, slog.New(slog.DiscardHandler))
	go m.serve()

	target, _ := url.Parse(fs.http.URL)
	front := httptest.NewServer(m.signaling(Config{}, target, pubPort))
	defer front.Close()

	type player struct {
		sock *net.UDPConn
		name string
	}
	var players []player
	for i := 0; i < 2; i++ {
		offer := "v=0\r\nm=application 9 UDP/DTLS/SCTP x\r\na=candidate:1 1 udp 1 10.0.0.5 4000 typ host\r\na=ice-ufrag:cl\r\n"
		resp, err := http.Post(front.URL+fmt.Sprintf("/v1/join/%d", 100+i), "application/sdp", strings.NewReader(offer))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		answer := string(body)
		if !strings.Contains(answer, fmt.Sprintf("127.0.0.1 %d typ host", pubPort)) || strings.Contains(answer, "a=candidate:1 1 udp 1 ") {
			t.Fatalf("answer not rewritten to the public port:\n%s", answer)
		}
		fs.mu.Lock()
		if strings.Contains(fs.offers[i], "a=candidate:") {
			t.Fatalf("offer reached the server with candidates:\n%s", fs.offers[i])
		}
		fs.mu.Unlock()

		c, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		players = append(players, player{c, fmt.Sprintf("srv%d", i)})
	}

	for i, p := range players {
		// the first packet identifies the player to the mux
		if _, err := p.sock.Write(stunRequest(p.name + ":cl")); err != nil {
			t.Fatal(err)
		}
		got, from := readFrom(t, fs.socks[i])
		if _, ok := stunUsername(got); !ok {
			t.Fatalf("player %d: server %d did not get the STUN check", i, i)
		}
		// the server's reply travels back through the public port
		fs.socks[i].WriteTo([]byte(fmt.Sprintf("pong-%d", i)), from)
		if reply, _ := readFrom(t, p.sock); string(reply) != fmt.Sprintf("pong-%d", i) {
			t.Fatalf("player %d got %q", i, reply)
		}
		// later packets (DTLS and so on) are not STUN but follow the same mapping
		if _, err := p.sock.Write([]byte(fmt.Sprintf("dtls-%d", i))); err != nil {
			t.Fatal(err)
		}
		if got, _ := readFrom(t, fs.socks[i]); string(got) != fmt.Sprintf("dtls-%d", i) {
			t.Fatalf("player %d data went to the wrong place: %q", i, got)
		}
	}

	// a stranger that never signaled gets nowhere
	stranger, _ := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	defer stranger.Close()
	stranger.Write([]byte("hello"))
	stranger.Write(stunRequest("nonexistent:x"))
	for i, s := range fs.socks {
		s.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if n, _, err := s.ReadFrom(make([]byte, 64)); err == nil {
			t.Fatalf("server port %d received %d unexpected bytes", i, n)
		}
	}
}
