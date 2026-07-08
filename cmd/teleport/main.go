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
	"teleport/internal/sync"
)

func main() {
	pass := flag.String("pass", "", "shared password for encryption (required)")
	name := flag.String("name", defaultName(), "device name")
	_ = flag.String("peer", "", "direct peer address host:port (skip discovery)")
	_ = flag.Int("port", 9878, "TCP listen port")
	verbose := flag.Bool("verbose", false, "enable debug logging")
	_ = flag.Bool("text-only", false, "sync text only, no files")
	_ = flag.Duration("poll-interval", 300*time.Millisecond, "clipboard poll interval (macOS only)")
	_ = flag.Bool("log-json", false, "output logs in JSON format")
	flag.Parse()

	if *pass == "" {
		fmt.Fprintln(os.Stderr, "error: -pass is required")
		flag.Usage()
		os.Exit(1)
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	logger.Warn("clipboard contents including passwords will be synced to peer")

	clip := clipboard.New(logger)

	// TODO: Phase 1 — initialize discovery, transport listener, connect components
	eng := sync.New(clip, nil, nil, *pass, *name, false, logger)

	// Graceful shutdown. os.Interrupt only — SIGTERM is not supported on Windows.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	logger.Info("started", "name", *name)
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
