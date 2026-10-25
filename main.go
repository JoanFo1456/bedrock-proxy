// bedrock-proxy is a launcher and proxy for Minecraft: Bedrock servers. Pterodactyl or Pelican starts this
// one executable: it runs the server found in server_dir on a loopback RakNet port and exposes it to
// players over NetherNet (TCP signaling + WebRTC UDP) on the single port the panel allocated.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := loadConfig("config.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	backendPort := int(cfg.BackendPort)
	backend := cfg.BackendAddress
	if backend == "" {
		if backendPort == 0 {
			if backendPort, err = freePort(); err != nil {
				log.Error("no free backend port", "err", err)
				os.Exit(1)
			}
		}
		backend = net.JoinHostPort("127.0.0.1", fmt.Sprint(backendPort))
		if cfg.BackendTransport != transportRakNet {
			backend = "http://" + backend // NetherNet addresses are signaling URLs
		}
	}

	// serverDone stays nil without a managed server; a nil channel blocks forever in the select below.
	var serverDone chan error
	if len(cfg.ServerCommand) > 0 {
		serverDone = make(chan error, 1)
		go func() { serverDone <- runServer(ctx, cfg, backendPort, log) }()
		// Give the server a moment to bind before the first player arrives.
		time.Sleep(500 * time.Millisecond)
	} else {
		log.Info("no server_command set, proxying to an externally managed backend", "backend", backend)
	}

	proxyCtx, cancelProxy := context.WithCancel(ctx)
	proxyErr := make(chan error, 1)
	go func() { proxyErr <- runProxy(proxyCtx, cfg, backend, log) }()

	var exit int
	select {
	case err := <-proxyErr:
		if err != nil {
			log.Error("proxy stopped", "err", err)
			exit = 1
		}
		stop()
	case err := <-serverDone:
		log.Info("server exited", "err", err)
		serverDone <- err // re-queue so the wait below still returns
		stop()
	case <-ctx.Done():
	}
	cancelProxy()
	if len(cfg.ServerCommand) > 0 {
		<-serverDone
	}
	os.Exit(exit)
}
