package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"time"

	"teleport/internal/clipboard"
	"teleport/internal/discovery"
	"teleport/internal/route"
	"teleport/internal/staging"
	"teleport/internal/sync"
	"teleport/internal/transport"
)

func main() {
	pass := flag.String("pass", "", "shared password for encryption (required)")
	name := flag.String("name", defaultName(), "device name")
	peerAddr := flag.String("peer", "", "direct peer address host:port (skip discovery)")
	port := flag.Int("port", 9878, "TCP listen port")
	verbose := flag.Bool("verbose", false, "enable debug logging")
	textOnly := flag.Bool("text-only", false, "sync text only, no files")
	pollInterval := flag.Duration("poll-interval", 300*time.Millisecond, "clipboard poll interval (macOS only)")
	bypassVPN := flag.Bool("bypass-vpn", false, "add direct route to peer IP bypassing VPN (requires admin/sudo)")
	logJSON := flag.Bool("log-json", false, "output logs in JSON format")
	flag.Parse()

	// Password: CLI flag takes priority, then env variable
	if *pass == "" {
		*pass = os.Getenv("TELEPORT_PASS")
	}
	if *pass == "" {
		fmt.Fprintln(os.Stderr, "error: -pass flag or TELEPORT_PASS env variable is required")
		flag.Usage()
		os.Exit(1)
	}

	// Logger
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	var handler slog.Handler
	if *logJSON {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}
	logger := slog.New(handler)

	logger.Warn("clipboard contents including passwords will be synced to peer")

	// Clipboard (poll interval is used on macOS, ignored on Windows)
	clip := clipboard.New(logger, *pollInterval)

	// Discovery
	var disc discovery.Discovery
	if *peerAddr != "" {
		disc = discovery.NewStatic(*peerAddr)
		logger.Info("using direct peer", "addr", *peerAddr)
	} else {
		disc = discovery.NewMulticast(*name, *port, logger)
	}

	// VPN bypass: detect local interface and bind connections to it
	var localAddr string
	if *bypassVPN {
		if *peerAddr == "" {
			logger.Error("-bypass-vpn requires -peer flag")
			os.Exit(1)
		}
		host, _, err := net.SplitHostPort(*peerAddr)
		if err != nil {
			logger.Error("invalid -peer address", "error", err)
			os.Exit(1)
		}
		directRoute, err := route.EnsureDirectRoute(host, logger)
		if err != nil {
			logger.Error("failed to add direct route (run as admin/sudo?)", "error", err)
			os.Exit(1)
		}
		defer directRoute.Remove()
		localAddr = directRoute.LocalIP
	}

	// Staging
	stager, err := staging.New(logger)
	if err != nil {
		logger.Error("failed to create staging directory", "error", err)
		os.Exit(1)
	}
	logger.Debug("staging directory", "path", stager.Path())

	// Transport Listener (bind to localAddr if VPN bypass, otherwise all interfaces)
	listenAddr := fmt.Sprintf(":%d", *port)
	if localAddr != "" {
		listenAddr = fmt.Sprintf("%s:%d", localAddr, *port)
	}
	lst, err := transport.NewListenerAddr(listenAddr, *pass, logger)
	if err != nil {
		logger.Error("failed to start listener", "error", err)
		os.Exit(1)
	}
	defer lst.Close()

	// Sync Engine
	eng := sync.New(clip, disc, lst, stager, *pass, *name, *textOnly, localAddr, logger)

	// Graceful shutdown. os.Interrupt only — SIGTERM is not supported on Windows.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Auto-cleanup staged files (older than 1h, check every 10min)
	stager.StartCleaner(ctx, 1*time.Hour, 10*time.Minute)

	logger.Info("started", "name", *name, "port", *port, "addr", lst.Addr())
	if err := eng.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
	logger.Info("stopped")
}

func defaultName() string {
	name, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return name
}
