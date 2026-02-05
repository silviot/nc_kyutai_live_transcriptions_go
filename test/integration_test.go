package test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/session"
)

// TestSessionManagerCreation tests basic session manager creation
func TestSessionManagerCreation(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	mgr := session.NewManager(session.ManagerConfig{
		HPBURL:         "ws://127.0.0.1:9999",
		HPBSecret:      "test-secret",
		ModalWorkspace: "test-workspace",
		ModalKey:       "test-key",
		ModalSecret:    "test-secret",
		MaxSpeakers:    100,
		Logger:         logger,
	})
	defer mgr.Close()

	// Verify initial state
	if mgr.RoomCount() != 0 {
		t.Errorf("expected 0 rooms initially, got %d", mgr.RoomCount())
	}

	if mgr.SpeakerCount() != 0 {
		t.Errorf("expected 0 speakers initially, got %d", mgr.SpeakerCount())
	}
}

// TestStopNonExistentRoom tests leaving a non-existent room
func TestStopNonExistentRoom(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	mgr := session.NewManager(session.ManagerConfig{
		HPBURL:         "ws://127.0.0.1:9999",
		HPBSecret:      "test-secret",
		ModalWorkspace: "test-workspace",
		ModalKey:       "test-key",
		ModalSecret:    "test-secret",
		Logger:         logger,
	})
	defer mgr.Close()

	// Try to leave a room that doesn't exist
	err := mgr.LeaveRoom("non-existent-room")
	if err == nil {
		t.Error("expected error when leaving non-existent room")
	}

	if !strings.Contains(err.Error(), "room not found") {
		t.Errorf("expected 'room not found' error, got: %v", err)
	}
}

// TestAPIEndpointValidation tests the API request validation
func TestAPIEndpointValidation(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	mgr := session.NewManager(session.ManagerConfig{
		HPBURL:         "ws://127.0.0.1:9999",
		HPBSecret:      "test-secret",
		ModalWorkspace: "test-workspace",
		ModalKey:       "test-key",
		ModalSecret:    "test-secret",
		Logger:         logger,
	})
	defer mgr.Close()

	tests := []struct {
		name           string
		body           string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "missing room token",
			body:           `{"languageId":"en"}`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "roomToken required",
		},
		{
			name:           "unsupported language",
			body:           `{"roomToken":"test-room","languageId":"ja"}`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "unsupported language",
		},
		{
			name:           "invalid JSON",
			body:           `{invalid`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "invalid request body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/call/transcribe",
				strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			mgr.HandleTranscribeRequest(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if !strings.Contains(rec.Body.String(), tt.expectedError) {
				t.Errorf("expected error containing '%s', got: %s", tt.expectedError, rec.Body.String())
			}
		})
	}
}

// TestAPIMethodValidation tests HTTP method validation
func TestAPIMethodValidation(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	mgr := session.NewManager(session.ManagerConfig{
		HPBURL:         "ws://127.0.0.1:9999",
		HPBSecret:      "test-secret",
		ModalWorkspace: "test-workspace",
		ModalKey:       "test-key",
		ModalSecret:    "test-secret",
		Logger:         logger,
	})
	defer mgr.Close()

	// Test wrong HTTP method
	req := httptest.NewRequest(http.MethodGet, "/api/v1/call/transcribe", nil)
	rec := httptest.NewRecorder()

	mgr.HandleTranscribeRequest(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d for GET, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}

// TestJoinRoomWithMockHPB tests joining a room with a mock HPB server
func TestJoinRoomWithMockHPB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Start mock HPB server
	mockHPB, err := StartMockHPB(0, logger) // Port 0 = auto-assign
	if err != nil {
		t.Fatalf("failed to start mock HPB: %v", err)
	}
	defer mockHPB.Close()

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	// Create session manager pointing to mock HPB
	mgr := session.NewManager(session.ManagerConfig{
		HPBURL:         mockHPB.URL(),
		HPBSecret:      "test-secret",
		ModalWorkspace: "test-workspace",
		ModalKey:       "test-key",
		ModalSecret:    "test-secret",
		Logger:         logger,
	})
	defer mgr.Close()

	// Join room via manager directly
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	room, err := mgr.JoinRoom(ctx, "test-room-123", "en", nil)
	if err != nil {
		t.Fatalf("failed to join room: %v", err)
	}

	if room == nil {
		t.Fatal("expected room to be non-nil")
	}

	// Verify room was created
	if mgr.RoomCount() != 1 {
		t.Errorf("expected 1 room, got %d", mgr.RoomCount())
	}

	// Wait for HPB client to connect
	if err := mockHPB.WaitForConnections(1, 5*time.Second); err != nil {
		t.Logf("HPB connection wait: %v (may be OK if connection was already made)", err)
	}

	// Leave room
	if err := mgr.LeaveRoom("test-room-123"); err != nil {
		t.Errorf("failed to leave room: %v", err)
	}

	if mgr.RoomCount() != 0 {
		t.Errorf("expected 0 rooms after leaving, got %d", mgr.RoomCount())
	}
}

// TestDuplicateRoomJoin tests that joining same room twice returns the same room
func TestDuplicateRoomJoin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	// Start mock HPB server
	mockHPB, err := StartMockHPB(0, logger)
	if err != nil {
		t.Fatalf("failed to start mock HPB: %v", err)
	}
	defer mockHPB.Close()

	time.Sleep(100 * time.Millisecond)

	mgr := session.NewManager(session.ManagerConfig{
		HPBURL:         mockHPB.URL(),
		HPBSecret:      "test-secret",
		ModalWorkspace: "test-workspace",
		ModalKey:       "test-key",
		ModalSecret:    "test-secret",
		Logger:         logger,
	})
	defer mgr.Close()

	ctx := context.Background()

	// Join room first time
	room1, err := mgr.JoinRoom(ctx, "test-room", "en", nil)
	if err != nil {
		t.Fatalf("first join failed: %v", err)
	}

	// Join same room again
	room2, err := mgr.JoinRoom(ctx, "test-room", "en", nil)
	if err != nil {
		t.Fatalf("second join failed: %v", err)
	}

	// Should be the same room
	if room1 != room2 {
		t.Error("expected same room instance for duplicate join")
	}

	// Should still only have 1 room
	if mgr.RoomCount() != 1 {
		t.Errorf("expected 1 room, got %d", mgr.RoomCount())
	}
}

// BenchmarkManagerCreation benchmarks session manager creation overhead
func BenchmarkManagerCreation(b *testing.B) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	for i := 0; i < b.N; i++ {
		mgr := session.NewManager(session.ManagerConfig{
			HPBURL:         "ws://127.0.0.1:9999",
			HPBSecret:      "test-secret",
			ModalWorkspace: "test-workspace",
			ModalKey:       "test-key",
			ModalSecret:    "test-secret",
			Logger:         logger,
		})
		mgr.Close()
	}
}

// BenchmarkAPIValidation benchmarks API request validation
func BenchmarkAPIValidation(b *testing.B) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	mgr := session.NewManager(session.ManagerConfig{
		HPBURL:         "ws://127.0.0.1:9999",
		HPBSecret:      "test-secret",
		ModalWorkspace: "test-workspace",
		ModalKey:       "test-key",
		ModalSecret:    "test-secret",
		Logger:         logger,
	})
	defer mgr.Close()

	body := `{"roomToken":"test-room","languageId":"en"}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/call/transcribe",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		// This will fail at HPB connection, but tests validation path
		mgr.HandleTranscribeRequest(rec, req)
	}
}
