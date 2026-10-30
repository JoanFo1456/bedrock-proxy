package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The mux lets players reach a NetherNet-only server through ONE public port, without terminating their
// connection. BDS opens one UDP port per player; the mux answers the HTTP signaling for it, rewrites the
// answer so it advertises the single public UDP address, and forwards each player's UDP packets to the
// internal port BDS allocated for them. The players still talk to BDS end to end (DTLS, identity and all),
// so BDS can keep online-mode on and sees the real player.
//
// Mapping: BDS's answer carries its ICE username fragment. A player's first UDP packet is a STUN check whose
// USERNAME is "<server ufrag>:<client ufrag>", which identifies the player's session. After that every packet
// from the same source address goes to the same internal port.

const (
	muxPendingTTL = time.Minute
	muxIdleTTL    = 90 * time.Second
	muxMaxPending = 4096
)

type muxSession struct {
	up    *net.UDPConn
	ufrag string

	mu     sync.Mutex
	client netip.AddrPort
	seen   time.Time
}

func (s *muxSession) touch(from netip.AddrPort) {
	s.mu.Lock()
	s.client, s.seen = from, time.Now()
	s.mu.Unlock()
}

func (s *muxSession) peer() (netip.AddrPort, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client, s.seen
}

type pendingSession struct {
	advertised netip.AddrPort // the public address the player was told to use
	from       string         // who asked for it
	internal   netip.AddrPort
	expires    time.Time
}

type udpMux struct {
	pub *net.UDPConn
	log *slog.Logger

	mu          sync.Mutex
	pending     map[string]pendingSession // by server ufrag, until the player's first STUN check arrives
	byAddr      map[netip.AddrPort]*muxSession
	byUfrag     map[string]*muxSession
	done        chan struct{}                // closed when serve returns
	lastUnknown map[netip.AddrPort]time.Time // rate limit for the unknown-session warning
}

func newUDPMux(pub *net.UDPConn, log *slog.Logger) *udpMux {
	return &udpMux{
		pub:         pub,
		log:         log,
		pending:     map[string]pendingSession{},
		byAddr:      map[netip.AddrPort]*muxSession{},
		byUfrag:     map[string]*muxSession{},
		done:        make(chan struct{}),
		lastUnknown: map[netip.AddrPort]time.Time{},
	}
}

// register records that a player with the given server ufrag will arrive for the internal address.
func (m *udpMux) register(ufrag string, internal, advertised netip.AddrPort, from string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) >= muxMaxPending {
		return errors.New("too many pending connections")
	}
	m.pending[ufrag] = pendingSession{internal: internal, advertised: advertised, from: from, expires: time.Now().Add(muxPendingTTL)}
	return nil
}

// serve reads the public socket until it is closed.
func (m *udpMux) serve() {
	defer close(m.done)
	go m.janitor()
	buf := make([]byte, 64*1024)
	for {
		n, from, err := m.pub.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		m.handle(buf[:n], from)
	}
}

func unmapped(a netip.AddrPort) netip.AddrPort { return netip.AddrPortFrom(a.Addr().Unmap(), a.Port()) }

func (m *udpMux) handle(pkt []byte, from netip.AddrPort) {
	key := unmapped(from)

	m.mu.Lock()
	if s := m.byAddr[key]; s != nil {
		m.mu.Unlock()
		s.touch(from)
		_, _ = s.up.Write(pkt)
		return
	}
	m.mu.Unlock()

	user, ok := stunUsername(pkt)
	if !ok {
		return // not from a known player and not a connectivity check: drop
	}
	server, _, _ := strings.Cut(user, ":")

	m.mu.Lock()
	s := m.byUfrag[server]
	if s == nil {
		p, ok := m.pending[server]
		if !ok || time.Now().After(p.expires) {
			warn := time.Since(m.lastUnknown[key]) > time.Minute
			if warn {
				m.lastUnknown[key] = time.Now()
			}
			m.mu.Unlock()
			if warn {
				m.log.Warn("connectivity check for a session the mux does not know (stale, or the player did not use this server's answer)", "from", key, "username", user)
			}
			return
		}
		up, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(p.internal))
		if err != nil {
			m.mu.Unlock()
			m.log.Error("mux: cannot reach the server's port for a player", "internal", p.internal, "err", err)
			return
		}
		s = &muxSession{up: up, ufrag: server}
		delete(m.pending, server)
		m.byUfrag[server] = s
		go m.downstream(s)
		m.log.Info("player connection mapped", "player_addr", key, "server_port", p.internal.Port())
	}
	// A new source address for a known session (a roaming player) is just another alias.
	m.byAddr[key] = s
	m.mu.Unlock()

	s.touch(from)
	_, _ = s.up.Write(pkt)
}

// downstream forwards the server's packets for one player back out of the public socket.
func (m *udpMux) downstream(s *muxSession) {
	buf := make([]byte, 64*1024)
	for {
		n, err := s.up.Read(buf)
		if err != nil {
			return
		}
		if client, _ := s.peer(); client.IsValid() {
			_, _ = m.pub.WriteToUDPAddrPort(buf[:n], client)
		}
	}
}

// janitor drops sessions that went quiet. WebRTC keeps a live session busy with consent checks, so a long
// silence means the player is gone.
func (m *udpMux) janitor() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-t.C:
		}
		now := time.Now()
		m.mu.Lock()
		for u, p := range m.pending {
			if now.After(p.expires) {
				m.log.Warn("player signaled but no UDP packet ever arrived: is UDP open on the public port, and is that address right?",
					"from", p.from, "told_to_send_udp_to", p.advertised)
				delete(m.pending, u)
			}
		}
		for a, t := range m.lastUnknown {
			if now.Sub(t) > time.Minute {
				delete(m.lastUnknown, a)
			}
		}
		for u, s := range m.byUfrag {
			if _, seen := s.peer(); now.Sub(seen) > muxIdleTTL {
				_ = s.up.Close()
				delete(m.byUfrag, u)
				for a, as := range m.byAddr {
					if as == s {
						delete(m.byAddr, a)
					}
				}
			}
		}
		m.mu.Unlock()
	}
}

// stunUsername returns the USERNAME attribute of a STUN binding request.
func stunUsername(p []byte) (string, bool) {
	if len(p) < 20 || p[0]&0xC0 != 0 || binary.BigEndian.Uint32(p[4:8]) != 0x2112A442 ||
		binary.BigEndian.Uint16(p[0:2]) != 0x0001 {
		return "", false
	}
	n := int(binary.BigEndian.Uint16(p[2:4]))
	if 20+n > len(p) {
		return "", false
	}
	b := p[20 : 20+n]
	for len(b) >= 4 {
		typ, l := binary.BigEndian.Uint16(b[0:2]), int(binary.BigEndian.Uint16(b[2:4]))
		if 4+l > len(b) {
			break
		}
		if typ == 0x0006 {
			return string(b[4 : 4+l]), true
		}
		adv := 4 + (l+3)&^3
		if adv > len(b) {
			break
		}
		b = b[adv:]
	}
	return "", false
}

// --- SDP handling -----------------------------------------------------------------------------------------

func sdpSplit(sdp string) []string { return strings.Split(strings.TrimRight(sdp, "\r\n"), "\r\n") }

// stripCandidates removes every ICE candidate from an offer, so the server learns the player's address from
// the player's own connectivity checks and never sends packets around the mux.
func stripCandidates(sdp string) string {
	var out []string
	for _, l := range sdpSplit(sdp) {
		if strings.HasPrefix(l, "a=candidate:") || l == "a=end-of-candidates" {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\r\n") + "\r\n"
}

// parseAnswer extracts the server's ICE username fragment and the address it listens on for this player.
func parseAnswer(sdp string) (ufrag string, internal netip.AddrPort, err error) {
	var best netip.AddrPort
	for _, l := range sdpSplit(sdp) {
		switch {
		case strings.HasPrefix(l, "a=ice-ufrag:"):
			ufrag = strings.TrimPrefix(l, "a=ice-ufrag:")
		case strings.HasPrefix(l, "a=candidate:"):
			f := strings.Fields(l)
			if len(f) < 8 || f[2] != "udp" {
				continue
			}
			ip, perr := netip.ParseAddr(f[4])
			port, perr2 := strconv.Atoi(f[5])
			if perr != nil || perr2 != nil || ip.IsLinkLocalUnicast() {
				continue
			}
			ap := netip.AddrPortFrom(ip, uint16(port))
			if !best.IsValid() || (ip.Is4() && !best.Addr().Is4()) {
				best = ap
			}
		}
	}
	if ufrag == "" {
		return "", netip.AddrPort{}, errors.New("answer has no ice-ufrag")
	}
	if !best.IsValid() {
		return "", netip.AddrPort{}, errors.New("answer has no usable UDP candidate")
	}
	return ufrag, best, nil
}

// rewriteAnswer points the answer at the public address and drops the server's own candidates.
func rewriteAnswer(sdp string, pub netip.AddrPort) string {
	ipType := "IP4"
	if pub.Addr().Is6() {
		ipType = "IP6"
	}
	var out []string
	for _, l := range sdpSplit(sdp) {
		switch {
		case strings.HasPrefix(l, "a=candidate:"):
			continue
		case strings.HasPrefix(l, "m=application "):
			f := strings.Fields(l)
			if len(f) >= 2 {
				f[1] = strconv.Itoa(int(pub.Port()))
				l = strings.Join(f, " ")
			}
		case strings.HasPrefix(l, "c=IN "):
			l = fmt.Sprintf("c=IN %s %s", ipType, pub.Addr())
			out = append(out, l, fmt.Sprintf("a=candidate:1 1 udp 2130706431 %s %d typ host generation 0", pub.Addr(), pub.Port()))
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\r\n") + "\r\n"
}

// --- signaling proxy --------------------------------------------------------------------------------------

type joinInfoKey struct{}

// joinInfo is what the signaling proxy remembers about a request between rewriting it and its answer.
type joinInfo struct{ host, remote string }

// publicAddr works out the address players should send UDP to: the configured public_ip, or the host the
// player used to reach the signaling endpoint.
func publicAddr(cfg Config, requestHost string, port int) (netip.AddrPort, error) {
	host := cfg.PublicIP
	if host == "" {
		h, _, err := net.SplitHostPort(requestHost)
		if err != nil {
			h = requestHost
		}
		host = strings.Trim(h, "[]")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return netip.AddrPortFrom(ip.Unmap(), uint16(port)), nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return netip.AddrPort{}, fmt.Errorf("cannot resolve %q: set public_ip in config.json", host)
	}
	best := ips[0]
	for _, ip := range ips {
		if ip.To4() != nil {
			best = ip
			break
		}
	}
	addr, _ := netip.AddrFromSlice(best)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), nil
}

func isJoin(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/join/")
}

func (m *udpMux) signaling(cfg Config, backend *url.URL, port int) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(backend)
			pr.Out.Host = backend.Host
			pr.Out = pr.Out.WithContext(context.WithValue(pr.Out.Context(), joinInfoKey{}, joinInfo{host: pr.In.Host, remote: pr.In.RemoteAddr}))
			if isJoin(pr.In) {
				body, _ := io.ReadAll(io.LimitReader(pr.In.Body, 1<<20))
				body = []byte(stripCandidates(string(body)))
				pr.Out.Body = io.NopCloser(bytes.NewReader(body))
				pr.Out.ContentLength = int64(len(body))
				pr.Out.Header.Del("Content-Length")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if !isJoin(resp.Request) || resp.StatusCode/100 != 2 {
				return nil
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if err != nil {
				return err
			}
			ufrag, internal, err := parseAnswer(string(body))
			if err != nil {
				return fmt.Errorf("server answer unusable: %w", err)
			}
			info, _ := resp.Request.Context().Value(joinInfoKey{}).(joinInfo)
			pub, err := publicAddr(cfg, info.host, port)
			if err != nil {
				return err
			}
			m.log.Info("player signaling", "from", info.remote, "told_to_send_udp_to", pub, "server_port", internal.Port())
			if err := m.register(ufrag, internal, pub, info.remote); err != nil {
				return err
			}
			out := []byte(rewriteAnswer(string(body), pub))
			resp.Body = io.NopCloser(bytes.NewReader(out))
			resp.ContentLength = int64(len(out))
			resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			m.log.Error("mux: signaling failed", "path", r.URL.Path, "err", err)
			http.Error(w, "server unavailable", http.StatusBadGateway)
		},
	}
	return rp
}

// runMux serves the signaling endpoint on TCP and the UDP forwarder on the same port number.
func runMux(ctx context.Context, cfg Config, backend string, log *slog.Logger) error {
	if cfg.BackendTransport == transportRakNet {
		return errors.New(`public_transport "mux" needs backend_transport "nethernet"`)
	}
	target, err := url.Parse(backend)
	if err != nil || target.Scheme != "http" {
		return fmt.Errorf("backend %q must be an http:// signaling URL", backend)
	}
	port := cfg.publicPort()
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(port))

	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}
	pub := pc.(*net.UDPConn)
	defer pub.Close()
	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen tcp: %w", err)
	}
	defer tcp.Close()

	mux := newUDPMux(pub, log)
	go mux.serve()
	srv := &http.Server{Handler: mux.signaling(cfg, target, port), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close(); _ = pub.Close() }()
	log.Info("proxy listening", "public", "mux", "tcp", port, "udp", port, "backend", backend)
	if err := srv.Serve(tcp); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
