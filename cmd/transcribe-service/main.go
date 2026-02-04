package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/session"
)

func main() {
	// Parse flags
	var (
		port            = flag.String("port", "8080", "HTTP server port")
		hpbURL          = flag.String("hpb-url", "", "HPB signaling server URL")
		hpbSecret       = flag.String("hpb-secret", "", "HPB HMAC secret")
		modalWorkspace  = flag.String("modal-workspace", "", "Modal workspace name")
		modalKey        = flag.String("modal-key", "", "Modal API key")
		modalSecret     = flag.String("modal-secret", "", "Modal API secret")
		logLevel        = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	)
	flag.Parse()

	// Load from environment if flags not set
	if *hpbURL == "" {
		*hpbURL = os.Getenv("LT_HPB_URL")
	}
	if *hpbSecret == "" {
		*hpbSecret = os.Getenv("LT_INTERNAL_SECRET")
	}
	if *modalWorkspace == "" {
		*modalWorkspace = os.Getenv("MODAL_WORKSPACE")
	}
	if *modalKey == "" {
		*modalKey = os.Getenv("MODAL_KEY")
	}
	if *modalSecret == "" {
		*modalSecret = os.Getenv("MODAL_SECRET")
	}

	// Validate required configuration
	if *hpbURL == "" || *hpbSecret == "" || *modalWorkspace == "" || *modalKey == "" || *modalSecret == "" {
		fmt.Fprintf(os.Stderr, "Error: Missing required environment variables:\n")
		fmt.Fprintf(os.Stderr, "  LT_HPB_URL\n")
		fmt.Fprintf(os.Stderr, "  LT_INTERNAL_SECRET\n")
		fmt.Fprintf(os.Stderr, "  MODAL_WORKSPACE\n")
		fmt.Fprintf(os.Stderr, "  MODAL_KEY\n")
		fmt.Fprintf(os.Stderr, "  MODAL_SECRET\n")
		os.Exit(1)
	}

	// Setup logging
	logger := setupLogger(*logLevel)

	logger.Info("starting transcription service",
		"port", *port,
		"hpb_url", *hpbURL,
		"modal_workspace", *modalWorkspace)

	// Create session manager
	sessionMgr := session.NewManager(session.ManagerConfig{
		HPBURL:         *hpbURL,
		HPBSecret:      *hpbSecret,
		ModalWorkspace: *modalWorkspace,
		ModalKey:       *modalKey,
		ModalSecret:    *modalSecret,
		MaxSpeakers:    500,
		Logger:         logger,
	})
	defer sessionMgr.Close()

	// Setup HTTP server
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"healthy","rooms":%d,"speakers":%d,"timestamp":%d}\n`,
			sessionMgr.RoomCount(), sessionMgr.SpeakerCount(), time.Now().Unix())
	})

	// Transcription endpoints
	mux.HandleFunc("POST /api/v1/call/transcribe", sessionMgr.HandleTranscribeRequest)
	mux.HandleFunc("DELETE /api/v1/call/transcribe/{roomToken}", sessionMgr.HandleStopTranscriptionRequest)

	// Metrics endpoint (placeholder)
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "# HELP transcription_rooms_active Number of active rooms\n")
		fmt.Fprintf(w, "# TYPE transcription_rooms_active gauge\n")
		fmt.Fprintf(w, "transcription_rooms_active %d\n", sessionMgr.RoomCount())
		fmt.Fprintf(w, "# HELP transcription_speakers_active Number of active speakers\n")
		fmt.Fprintf(w, "# TYPE transcription_speakers_active gauge\n")
		fmt.Fprintf(w, "transcription_speakers_active %d\n", sessionMgr.SpeakerCount())
	})

	server := &http.Server{
		Addr:    ":" + *port,
		Handler: mux,
	}

	// Start server in goroutine
	go func() {
		logger.Info("HTTP server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutdown signal received, gracefully shutting down")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}

	logger.Info("transcription service stopped")
}

// setupLogger creates a structured logger
func setupLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: lvl,
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
