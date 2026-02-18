package nextcloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Client manages communication with Nextcloud OCS API
type Client struct {
	baseURL      string
	roomToken    string
	httpClient   *http.Client
	requestToken string
	sessionID    string
	logger       *slog.Logger
}

// Config holds OCS client configuration
type Config struct {
	RoomURL string       // Full room URL (e.g., https://cloud.example.com/call/token)
	Logger  *slog.Logger
}

// NewClient creates a new Nextcloud OCS client
func NewClient(cfg Config) (*Client, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	baseURL, roomToken, err := parseRoomURL(cfg.RoomURL)
	if err != nil {
		return nil, fmt.Errorf("invalid room URL: %w", err)
	}

	// Create cookie jar for session management
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	return &Client{
		baseURL:   baseURL,
		roomToken: roomToken,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
		logger: cfg.Logger,
	}, nil
}

// parseRoomURL extracts base URL and room token from a room URL
func parseRoomURL(roomURL string) (baseURL, token string, err error) {
	u, err := url.Parse(roomURL)
	if err != nil {
		return "", "", err
	}

	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("invalid URL: missing scheme or host")
	}

	// Extract token from path (e.g., /call/erwcr27x)
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return "", "", fmt.Errorf("no room token in path")
	}
	token = parts[len(parts)-1]

	baseURL = fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	return baseURL, token, nil
}

// Initialize fetches the requesttoken and establishes a session
func (c *Client) Initialize() error {
	// First fetch the room page to establish session cookies
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/call/%s", c.baseURL, c.roomToken))
	if err != nil {
		return fmt.Errorf("failed to fetch room page: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("failed to read room page: %w", err)
	}

	// Try to extract requesttoken from HTML
	re := regexp.MustCompile(`data-requesttoken="([^"]*)"`)
	matches := re.FindSubmatch(body)
	if len(matches) >= 2 && string(matches[1]) != "" {
		c.requestToken = string(matches[1])
		c.logger.Debug("obtained requesttoken from page", "token", c.requestToken[:min(20, len(c.requestToken))]+"...")
		return nil
	}

	// Fallback: fetch CSRF token from API (uses same session cookies)
	c.logger.Debug("requesttoken empty on page, fetching from CSRF endpoint")
	csrfResp, err := c.httpClient.Get(fmt.Sprintf("%s/index.php/csrftoken", c.baseURL))
	if err != nil {
		return fmt.Errorf("failed to fetch CSRF token: %w", err)
	}
	csrfBody, err := io.ReadAll(csrfResp.Body)
	csrfResp.Body.Close()
	if err != nil {
		return fmt.Errorf("failed to read CSRF response: %w", err)
	}

	var csrfData struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(csrfBody, &csrfData); err == nil && csrfData.Token != "" {
		c.requestToken = csrfData.Token
		c.logger.Debug("obtained requesttoken from CSRF endpoint", "token", c.requestToken[:min(20, len(c.requestToken))]+"...")
		return nil
	}

	c.logger.Warn("no requesttoken obtained, API calls may fail")
	return nil
}

// JoinAsGuest joins the room as a guest participant
func (c *Client) JoinAsGuest() (*Participant, error) {
	path := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active?format=json", c.roomToken)
	data, err := c.ocsPost(path, map[string]interface{}{"force": true})
	if err != nil {
		return nil, fmt.Errorf("failed to join as guest: %w", err)
	}

	var participant Participant
	if err := json.Unmarshal(data, &participant); err != nil {
		return nil, fmt.Errorf("failed to parse participant data: %w", err)
	}

	c.sessionID = participant.SessionID
	c.logger.Info("joined room as guest", "sessionID", c.sessionID)
	return &participant, nil
}

// SetGuestName sets the display name for the guest
func (c *Client) SetGuestName(displayName string) error {
	path := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/guest/%s/name?format=json", c.roomToken)
	_, err := c.ocsPost(path, map[string]interface{}{"displayName": displayName})
	if err != nil {
		c.logger.Warn("failed to set guest name", "error", err)
		return err
	}
	c.logger.Debug("set guest name", "name", displayName)
	return nil
}

// GetSignalingSettings fetches signaling configuration
func (c *Client) GetSignalingSettings() (*SignalingSettings, error) {
	path := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v3/signaling/settings?format=json&token=%s", c.roomToken)
	data, err := c.ocsGet(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get signaling settings: %w", err)
	}

	var settings SignalingSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse signaling settings: %w", err)
	}

	c.logger.Info("got signaling settings", "server", settings.Server, "stunServers", len(settings.STUNServers), "turnServers", len(settings.TURNServers))
	c.logger.Debug("raw signaling settings", "data", string(data))
	return &settings, nil
}

// JoinCall joins the audio/video call
func (c *Client) JoinCall() error {
	path := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s?format=json", c.roomToken)
	body := map[string]interface{}{
		"flags":            3, // Audio + Video
		"silent":           false,
		"recordingConsent": false,
		"silentFor":        []interface{}{},
	}
	_, err := c.ocsPost(path, body)
	if err != nil {
		return fmt.Errorf("failed to join call: %w", err)
	}
	c.logger.Info("joined call")
	return nil
}

// LeaveCall leaves the audio/video call
func (c *Client) LeaveCall() error {
	path := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s?format=json", c.roomToken)
	_, err := c.ocsDelete(path)
	if err != nil {
		c.logger.Warn("failed to leave call", "error", err)
		return err
	}
	c.logger.Info("left call")
	return nil
}

// EnableTranscription enables live transcription for the room
func (c *Client) EnableTranscription(language string) error {
	if language == "" {
		language = "en"
	}

	// Set language first
	langPath := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/live-transcription/%s/language?format=json", c.roomToken)
	_, err := c.ocsPost(langPath, map[string]interface{}{"languageId": language})
	if err != nil {
		c.logger.Warn("failed to set transcription language", "error", err)
	} else {
		c.logger.Debug("set transcription language", "language", language)
	}

	// Enable transcription
	enablePath := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/live-transcription/%s?format=json", c.roomToken)
	_, err = c.ocsPost(enablePath, map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to enable transcription: %w", err)
	}
	c.logger.Info("enabled live transcription", "language", language)
	return nil
}

// DisableTranscription disables live transcription for the room
func (c *Client) DisableTranscription() error {
	path := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/live-transcription/%s?format=json", c.roomToken)
	_, err := c.ocsDelete(path)
	if err != nil {
		c.logger.Warn("failed to disable transcription", "error", err)
		return err
	}
	c.logger.Info("disabled live transcription")
	return nil
}

// RoomToken returns the room token
func (c *Client) RoomToken() string {
	return c.roomToken
}

// SessionID returns the participant session ID
func (c *Client) SessionID() string {
	return c.sessionID
}

// BaseURL returns the base Nextcloud URL
func (c *Client) BaseURL() string {
	return c.baseURL
}

// ocsPost makes an OCS POST request
func (c *Client) ocsPost(path string, body map[string]interface{}) (json.RawMessage, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OCS-APIREQUEST", "true")
	if c.requestToken != "" {
		req.Header.Set("requesttoken", c.requestToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Parse OCS response
	var ocsResp OCSResponse
	if err := json.Unmarshal(respBody, &ocsResp); err != nil {
		return nil, fmt.Errorf("failed to parse OCS response: %w (body: %s)", err, string(respBody))
	}

	if ocsResp.OCS.Meta.StatusCode != 200 {
		return nil, fmt.Errorf("OCS error: %s (code: %d)", ocsResp.OCS.Meta.Message, ocsResp.OCS.Meta.StatusCode)
	}

	return ocsResp.OCS.Data, nil
}

// ocsGet makes an OCS GET request
func (c *Client) ocsGet(path string) (json.RawMessage, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("OCS-APIREQUEST", "true")
	if c.requestToken != "" {
		req.Header.Set("requesttoken", c.requestToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var ocsResp OCSResponse
	if err := json.Unmarshal(respBody, &ocsResp); err != nil {
		return nil, fmt.Errorf("failed to parse OCS response: %w (body: %s)", err, string(respBody))
	}

	if ocsResp.OCS.Meta.StatusCode != 200 {
		return nil, fmt.Errorf("OCS error: %s (code: %d)", ocsResp.OCS.Meta.Message, ocsResp.OCS.Meta.StatusCode)
	}

	return ocsResp.OCS.Data, nil
}

// ocsDelete makes an OCS DELETE request
func (c *Client) ocsDelete(path string) (json.RawMessage, error) {
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("OCS-APIREQUEST", "true")
	if c.requestToken != "" {
		req.Header.Set("requesttoken", c.requestToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var ocsResp OCSResponse
	if err := json.Unmarshal(respBody, &ocsResp); err != nil {
		return nil, fmt.Errorf("failed to parse OCS response: %w (body: %s)", err, string(respBody))
	}

	return ocsResp.OCS.Data, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
