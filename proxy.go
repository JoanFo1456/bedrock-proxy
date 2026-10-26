package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/df-mc/go-nethernet"
	"github.com/df-mc/go-nethernet/endpoint"
	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The shared-UDP-port NetherNet listener below follows the approach used by Portal
// (github.com/MEMOxiiii/portal, Apache-2.0): one pion UDP mux serves every WebRTC session.

const (
	transportRakNet    = "raknet"
	transportNetherNet = "nethernet"
)

// netherNetServer owns the HTTP signaling endpoint and the UDP mux backing the single media port.
type netherNetServer struct {
	http *http.Server
	tcp  net.Listener
	mux  *ice.MultiUDPMuxDefault
}

func (s *netherNetServer) Close() error {
	err := s.http.Close()
	if e := s.tcp.Close(); e != nil && !errors.Is(e, net.ErrClosed) {
		err = errors.Join(err, e)
	}
	return errors.Join(err, s.mux.Close())
}

// newNetherNet builds the signaling endpoint on TCP host:port and a minecraft.Network whose WebRTC media
// all flows through UDP port (the same number), so a single allocated port is enough.
func newNetherNet(host string, port int, iceURLs []string, key *ecdsa.PrivateKey, log *slog.Logger) (minecraft.Network, *netherNetServer, error) {
	// Link-local addresses (fe80::/10, 169.254/16) often cannot be bound inside containers, so skip them.
	mux, err := ice.NewMultiUDPMuxFromPort(port, ice.UDPMuxFromPortWithIPFilter(func(ip net.IP) bool {
		return !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
	}))
	if err != nil {
		return nil, nil, fmt.Errorf("udp mux on port %d: %w", port, err)
	}
	settings := webrtc.SettingEngine{}
	settings.SetICEUDPMux(mux)

	var servers []nethernet.ICEServer
	if len(iceURLs) > 0 {
		servers = []nethernet.ICEServer{{URLs: iceURLs}}
	}
	creds := &nethernet.Credentials{ICEServers: servers}
	handler := endpoint.HandlerConfig{
		Logger:      log,
		Credentials: func(context.Context) (*nethernet.Credentials, error) { return creds, nil },
	}.New()

	tcp, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		_ = mux.Close()
		return nil, nil, fmt.Errorf("signaling listener: %w", err)
	}

	network := minecraft.NetherNet{
		Signaling: handler,
		Log:       log,
		ListenConfig: nethernet.ListenConfig{
			API:                 webrtc.NewAPI(webrtc.WithSettingEngine(settings)),
			IssueServerIdentity: issueIdentity(key),
		},
	}
	srv := &netherNetServer{
		http: &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second},
		tcp:  tcp,
		mux:  mux,
	}
	return network, srv, nil
}

// runProxy accepts players on the public transport and relays each to the backend until ctx is cancelled.
func runProxy(ctx context.Context, cfg Config, backend string, log *slog.Logger) error {
	port := cfg.publicPort()
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(port))

	key, err := loadIdentityKey(cfg.IdentityKeyFile)
	if err != nil {
		return fmt.Errorf("identity key: %w", err)
	}

	// AuthenticationDisabled must stay false: the proxy is what authenticates players, because the
	// backend runs with online-mode=false and trusts whatever identity the proxy presents.
	lc := minecraft.ListenConfig{
		ErrorLog:       log,
		MaximumPlayers: int(cfg.MaxPlayers),
		StatusProvider: status{motd: cfg.Motd},
	}
	if os.Getenv("BEDROCK_PROXY_INSECURE_TEST_NO_AUTH") == "1" {
		// For automated tests only: lets clients without an Xbox account join, with any name.
		log.Warn("PLAYER AUTHENTICATION IS DISABLED (BEDROCK_PROXY_INSECURE_TEST_NO_AUTH=1); never use this on a public server")
		lc.AuthenticationDisabled = true
	}

	var l *minecraft.Listener
	switch cfg.PublicTransport {
	case transportNetherNet:
		network, nn, err := newNetherNet(cfg.Host, port, cfg.ICEServers, key, log)
		if err != nil {
			return err
		}
		defer nn.Close()
		if l, err = lc.ListenNetwork(network, addr); err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		// The signaling handler rejects offers until the listener is registered, so serve only now.
		go func() { _ = nn.http.Serve(nn.tcp) }()
		log.Info("proxy listening", "public", "nethernet", "tcp", port, "udp", port, "backend", backend)
	default:
		if l, err = lc.Listen(transportRakNet, addr); err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		log.Info("proxy listening", "public", "raknet", "udp", port, "backend", backend)
	}
	defer l.Close()

	go func() { <-ctx.Done(); _ = l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handlePlayer(ctx, c.(*minecraft.Conn), cfg, backend, key, log)
	}
}

// dialBackend connects to the backend as the given player. For a NetherNet backend the proxy signs its own
// identity token, which a backend with online-mode=false accepts.
func dialBackend(ctx context.Context, cfg Config, backend string, key *ecdsa.PrivateKey, id login.IdentityData, cd login.ClientData, log *slog.Logger) (*minecraft.Conn, error) {
	d := minecraft.Dialer{IdentityData: id, ClientData: cd, ErrorLog: log}
	if cfg.BackendTransport == transportRakNet {
		return d.DialContext(ctx, transportRakNet, backend)
	}
	ident, err := nethernet.GenerateServerIdentity(key, "self")
	if err != nil {
		return nil, fmt.Errorf("create identity: %w", err)
	}
	signaling := endpoint.NewClient()
	if cfg.Debug {
		signaling = endpoint.ClientConfig{HTTPClient: &http.Client{Transport: debugTransport{log: log}}}.New()
	}
	n := minecraft.NetherNet{Log: log, Signaling: signaling}
	n.Dialer.Identity = ident
	log.Debug("dialing backend", "backend", backend, "identity_domain", ident.Domain, "token_len", len(ident.Token))
	return d.DialContextNetwork(ctx, n, backend)
}

// handlePlayer connects one player to the backend, presenting the identity the proxy already verified.
func handlePlayer(ctx context.Context, client *minecraft.Conn, cfg Config, backend string, key *ecdsa.PrivateKey, log *slog.Logger) {
	id := client.IdentityData()
	log = log.With("player", id.DisplayName, "xuid", id.XUID)
	defer client.Close()

	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	server, err := dialBackend(dctx, cfg, backend, key, id, client.ClientData(), log)
	cancel()
	if err != nil {
		log.Error("backend dial failed", "err", err)
		if cfg.Debug {
			logNetDiagnostics(log)
			probeOfferVariants(log)
		}
		_ = client.WritePacket(&packet.Disconnect{Message: "Server is unavailable, try again shortly."})
		return
	}
	defer server.Close()

	var wg sync.WaitGroup
	var startErr, spawnErr error
	wg.Add(2)
	go func() { defer wg.Done(); startErr = client.StartGame(server.GameData()) }()
	go func() { defer wg.Done(); spawnErr = server.DoSpawn() }()
	wg.Wait()
	if err := errors.Join(startErr, spawnErr); err != nil {
		log.Error("join failed", "err", err)
		return
	}
	log.Info("player joined")
	defer log.Info("player left")

	done := make(chan struct{}, 2)
	relay := func(from, to *minecraft.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			pk, err := from.ReadPacket()
			if err != nil {
				var dc minecraft.DisconnectError
				if errors.As(err, &dc) {
					_ = to.WritePacket(&packet.Disconnect{Message: dc.Error()})
				}
				return
			}
			if err := to.WritePacket(pk); err != nil {
				return
			}
		}
	}
	go relay(client, server)
	go relay(server, client)
	select {
	case <-done:
	case <-ctx.Done():
	}
}
