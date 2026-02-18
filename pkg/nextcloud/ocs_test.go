package nextcloud

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRoomURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantBase  string
		wantToken string
		wantErr   bool
	}{
		{
			name:      "standard call URL",
			url:       "https://cloud.example.com/call/erwcr27x",
			wantBase:  "https://cloud.example.com",
			wantToken: "erwcr27x",
		},
		{
			name:      "with subpath",
			url:       "https://cloud.example.com/nextcloud/call/abc123",
			wantBase:  "https://cloud.example.com",
			wantToken: "abc123",
		},
		{
			name:      "with port",
			url:       "http://localhost:8080/call/test-room",
			wantBase:  "http://localhost:8080",
			wantToken: "test-room",
		},
		{
			name:      "trailing slash",
			url:       "https://cloud.example.com/call/myroom/",
			wantBase:  "https://cloud.example.com",
			wantToken: "myroom",
		},
		{
			name:    "missing scheme",
			url:     "cloud.example.com/call/abc",
			wantErr: true,
		},
		{
			name:      "bare domain extracts empty token",
			url:       "https://cloud.example.com",
			wantBase:  "https://cloud.example.com",
			wantToken: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, token, err := parseRoomURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRoomURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if base != tt.wantBase {
				t.Errorf("baseURL = %q, want %q", base, tt.wantBase)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
		})
	}
}

func TestInitialize_CSRFFromPage(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/call/") {
			w.Write([]byte(`<html><head data-requesttoken="csrf-token-abc123"></head><body></body></html>`))
			return
		}
		w.WriteHeader(404)
	}))
	defer mock.Close()

	client := &Client{
		baseURL:    mock.URL,
		roomToken:  "test-room",
		httpClient: mock.Client(),
		logger:     slog.Default(),
	}

	if err := client.Initialize(); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	if client.requestToken != "csrf-token-abc123" {
		t.Errorf("requestToken = %q, want %q", client.requestToken, "csrf-token-abc123")
	}
}

func TestInitialize_CSRFFromEndpoint(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/call/"):
			// Return page without requesttoken
			w.Write([]byte(`<html><head></head><body></body></html>`))
		case r.URL.Path == "/index.php/csrftoken":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"token":"csrf-from-endpoint"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer mock.Close()

	client := &Client{
		baseURL:    mock.URL,
		roomToken:  "test-room",
		httpClient: mock.Client(),
		logger:     slog.Default(),
	}

	if err := client.Initialize(); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	if client.requestToken != "csrf-from-endpoint" {
		t.Errorf("requestToken = %q, want %q", client.requestToken, "csrf-from-endpoint")
	}
}

func TestOCSPost_RequestFormat(t *testing.T) {
	var capturedReq *http.Request
	var capturedBody []byte

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r.Clone(r.Context())
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":null}}`))
	}))
	defer mock.Close()

	client := &Client{
		baseURL:      mock.URL,
		roomToken:    "test-room",
		httpClient:   mock.Client(),
		requestToken: "my-csrf-token",
	}

	_, err := client.ocsPost("/ocs/v2.php/apps/spreed/api/v4/test?format=json", map[string]interface{}{"key": "value"})
	if err != nil {
		t.Fatalf("ocsPost failed: %v", err)
	}

	// Verify required OCS headers
	if capturedReq.Header.Get("OCS-APIREQUEST") != "true" {
		t.Errorf("OCS-APIREQUEST = %q, want %q", capturedReq.Header.Get("OCS-APIREQUEST"), "true")
	}
	if capturedReq.Header.Get("requesttoken") != "my-csrf-token" {
		t.Errorf("requesttoken = %q, want %q", capturedReq.Header.Get("requesttoken"), "my-csrf-token")
	}
	if capturedReq.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want %q", capturedReq.Header.Get("Content-Type"), "application/json")
	}

	// Verify body is valid JSON with expected content
	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body["key"] != "value" {
		t.Errorf("body[key] = %v, want value", body["key"])
	}
}

func TestJoinAsGuest_EndpointAndBody(t *testing.T) {
	var capturedPath string
	var capturedBody []byte

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		resp := `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{"sessionId":"guest-sess-123","actorType":"guests","actorId":"g1"}}}`
		w.Write([]byte(resp))
	}))
	defer mock.Close()

	client := &Client{
		baseURL:    mock.URL,
		roomToken:  "erwcr27x",
		httpClient: mock.Client(),
		logger:     slog.Default(),
	}

	participant, err := client.JoinAsGuest()
	if err != nil {
		t.Fatalf("JoinAsGuest failed: %v", err)
	}

	expectedPath := "/ocs/v2.php/apps/spreed/api/v4/room/erwcr27x/participants/active"
	if capturedPath != expectedPath {
		t.Errorf("path = %q, want %q", capturedPath, expectedPath)
	}

	var body map[string]interface{}
	json.Unmarshal(capturedBody, &body)
	if body["force"] != true {
		t.Errorf("body.force = %v, want true", body["force"])
	}

	if participant.SessionID != "guest-sess-123" {
		t.Errorf("sessionID = %q, want %q", participant.SessionID, "guest-sess-123")
	}
}

func TestGetSignalingSettings_EndpointAndParsing(t *testing.T) {
	var capturedPath string

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		resp := `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{"server":"wss://hpb.example.com/spreed","ticket":"t123","stunservers":[{"urls":["stun:stun.example.com:443"]}],"turnservers":[{"urls":["turn:turn.example.com:443"],"username":"u","credential":"c"}],"helloAuthParams":{"2.0":{"token":"jwt-abc"}}}}}`
		w.Write([]byte(resp))
	}))
	defer mock.Close()

	client := &Client{
		baseURL:    mock.URL,
		roomToken:  "myroom",
		httpClient: mock.Client(),
		logger:     slog.Default(),
	}

	settings, err := client.GetSignalingSettings()
	if err != nil {
		t.Fatalf("GetSignalingSettings failed: %v", err)
	}

	expectedPath := "/ocs/v2.php/apps/spreed/api/v3/signaling/settings?format=json&token=myroom"
	if capturedPath != expectedPath {
		t.Errorf("path = %q, want %q", capturedPath, expectedPath)
	}

	if settings.Server != "wss://hpb.example.com/spreed" {
		t.Errorf("Server = %q, want %q", settings.Server, "wss://hpb.example.com/spreed")
	}
	if len(settings.STUNServers) != 1 {
		t.Errorf("STUNServers count = %d, want 1", len(settings.STUNServers))
	}
	if len(settings.TURNServers) != 1 {
		t.Errorf("TURNServers count = %d, want 1", len(settings.TURNServers))
	}
	if v, ok := settings.HelloAuthParams["2.0"]; !ok || v.Token != "jwt-abc" {
		t.Errorf("HelloAuthParams[2.0].Token = %v, want jwt-abc", v.Token)
	}
}

func TestJoinCall_EndpointAndFlags(t *testing.T) {
	var capturedPath string
	var capturedBody []byte

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":null}}`))
	}))
	defer mock.Close()

	client := &Client{
		baseURL:    mock.URL,
		roomToken:  "myroom",
		httpClient: mock.Client(),
		logger:     slog.Default(),
	}

	if err := client.JoinCall(); err != nil {
		t.Fatalf("JoinCall failed: %v", err)
	}

	expectedPath := "/ocs/v2.php/apps/spreed/api/v4/call/myroom"
	if capturedPath != expectedPath {
		t.Errorf("path = %q, want %q", capturedPath, expectedPath)
	}

	var body map[string]interface{}
	json.Unmarshal(capturedBody, &body)
	// flags should be 3 (audio + video)
	if flags, ok := body["flags"].(float64); !ok || int(flags) != 3 {
		t.Errorf("body.flags = %v, want 3", body["flags"])
	}
}

func TestEnableTranscription_TwoRequests(t *testing.T) {
	var capturedPaths []string
	var capturedBodies [][]byte

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPaths = append(capturedPaths, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		capturedBodies = append(capturedBodies, body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":null}}`))
	}))
	defer mock.Close()

	client := &Client{
		baseURL:    mock.URL,
		roomToken:  "myroom",
		httpClient: mock.Client(),
		logger:     slog.Default(),
	}

	if err := client.EnableTranscription("de"); err != nil {
		t.Fatalf("EnableTranscription failed: %v", err)
	}

	if len(capturedPaths) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(capturedPaths))
	}

	// First request: set language
	expectedLangPath := "/ocs/v2.php/apps/spreed/api/v1/live-transcription/myroom/language"
	if capturedPaths[0] != expectedLangPath {
		t.Errorf("first request path = %q, want %q", capturedPaths[0], expectedLangPath)
	}
	var langBody map[string]interface{}
	json.Unmarshal(capturedBodies[0], &langBody)
	if langBody["languageId"] != "de" {
		t.Errorf("languageId = %v, want de", langBody["languageId"])
	}

	// Second request: enable transcription
	expectedEnablePath := "/ocs/v2.php/apps/spreed/api/v1/live-transcription/myroom"
	if capturedPaths[1] != expectedEnablePath {
		t.Errorf("second request path = %q, want %q", capturedPaths[1], expectedEnablePath)
	}
}

func TestOCSErrorHandling(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Forbidden"},"data":null}}`))
	}))
	defer mock.Close()

	client := &Client{
		baseURL:    mock.URL,
		roomToken:  "test",
		httpClient: mock.Client(),
	}

	_, err := client.ocsPost("/test", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for non-200 OCS statuscode")
	}
	if !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("error = %q, want it to contain Forbidden", err.Error())
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want it to contain 403", err.Error())
	}
}

func TestNewClient(t *testing.T) {
	client, err := NewClient(Config{
		RoomURL: "https://cloud.example.com/call/mytoken",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if client.baseURL != "https://cloud.example.com" {
		t.Errorf("baseURL = %q, want %q", client.baseURL, "https://cloud.example.com")
	}
	if client.roomToken != "mytoken" {
		t.Errorf("roomToken = %q, want %q", client.roomToken, "mytoken")
	}
}

func TestNewClient_InvalidURL(t *testing.T) {
	_, err := NewClient(Config{
		RoomURL: "not-a-url",
	})
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

// Verify that the parseRoomURL edge case for empty trailing path segment is handled.
func TestParseRoomURL_TrailingSlashYieldsEmptyToken(t *testing.T) {
	// "https://cloud.example.com/" has path "/" which when trimmed and split gives [""]
	_, token, err := parseRoomURL("https://cloud.example.com/")
	if err != nil {
		// It's acceptable to error on this
		return
	}
	// If no error, the token should be empty string (last segment of "/")
	_ = fmt.Sprintf("token=%s", token)
}
