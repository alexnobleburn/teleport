package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"teleport/internal/clipboard"
	"teleport/internal/discovery"
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
	logJSON := flag.Bool("log-json", false, "output logs in JSON format")
	flag.Parse()

	if *pass == "" {
		fmt.Fprintln(os.Stderr, "error: -pass is required")
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

	// Transport Listener
	lst, err := transport.NewListener(*port, *pass, logger)
	if err != nil {
		logger.Error("failed to start listener", "error", err)
		os.Exit(1)
	}
	defer lst.Close()

	// Sync Engine
	eng := sync.New(clip, disc, lst, *pass, *name, *textOnly, logger)

	// Graceful shutdown. os.Interrupt only — SIGTERM is not supported on Windows.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

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
