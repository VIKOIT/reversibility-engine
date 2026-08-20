package github_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gh "github.com/VIKOIT/reversibility-engine/internal/delivery/github"
)

func writePEMKey(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "key.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
	return path
}

func TestNewServerWithAToken(t *testing.T) {
	t.Parallel()

	server, err := gh.NewServer(gh.Config{
		Addr:          ":0",
		WebhookSecret: testSecret,
		Token:         "a-token",
	}, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if server.Handler == nil {
		t.Fatal("server has no handler")
	}

	// Timeouts are the cheapest defence against an unauthenticated client holding a connection
	// open, on a server that has to stay reachable.
	if server.ReadHeaderTimeout == 0 || server.ReadTimeout == 0 || server.WriteTimeout == 0 {
		t.Error("the server has no read or write timeouts")
	}
}

func TestNewServerWithAppCredentials(t *testing.T) {
	t.Parallel()

	keyPath := writePEMKey(t)
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading the key: %v", err)
	}

	if _, err := gh.NewServer(gh.Config{
		Addr:          ":0",
		WebhookSecret: testSecret,
		AppID:         1234,
		PrivateKey:    key,
	}, nil); err != nil {
		t.Fatalf("NewServer: %v", err)
	}
}

func TestNewServerRejectsBadCredentials(t *testing.T) {
	t.Parallel()

	tests := map[string]gh.Config{
		"no credentials at all": {Addr: ":0", WebhookSecret: testSecret},
		"app ID with no key":    {Addr: ":0", WebhookSecret: testSecret, AppID: 1},
		"unparseable key":       {Addr: ":0", WebhookSecret: testSecret, AppID: 1, PrivateKey: []byte("not a key")},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := gh.NewServer(cfg, nil); err == nil {
				t.Error("the server was built with unusable credentials")
			}
		})
	}
}

// The routes are the server's whole surface. A webhook path that silently stopped matching would
// leave every delivery unanswered.
func TestServerRoutes(t *testing.T) {
	t.Parallel()

	server, err := gh.NewServer(gh.Config{Addr: ":0", WebhookSecret: testSecret, Token: "t"}, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"health check", http.MethodGet, "/healthz", http.StatusOK},
		{"unsigned webhook", http.MethodPost, "/webhook", http.StatusUnauthorized},
		{"webhook by GET", http.MethodGet, "/webhook", http.StatusMethodNotAllowed},
		{"unknown path", http.MethodGet, "/", http.StatusNotFound},
		{"health check by POST", http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequest(tt.method, tt.path, nil)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}

			rec := httptest.NewRecorder()
			server.Handler.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.want)
			}
		})
	}
}

// Run must serve until its context is cancelled and then shut down cleanly, because analysis
// continues after the HTTP response and an abrupt exit drops certificates GitHub will not resend.
func TestRunServesAndShutsDownCleanly(t *testing.T) {
	t.Parallel()

	// A port is chosen by the OS so the test cannot collide with anything.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- gh.Run(ctx, gh.Config{Addr: addr, WebhookSecret: testSecret, Token: "t"}, nil)
	}()

	// Wait for the listener to come up rather than sleeping a fixed amount.
	if err := waitForServer(addr); err != nil {
		cancel()
		t.Fatalf("the server never accepted a connection: %v", err)
	}

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		cancel()
		t.Fatalf("health check: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("health check = %d, want 200", resp.StatusCode)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned an error on shutdown: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("Run did not return after its context was cancelled")
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	t.Parallel()

	err := gh.Run(context.Background(), gh.Config{Addr: ":0", WebhookSecret: testSecret}, nil)
	if err == nil {
		t.Error("Run started with no credentials")
	}
}

// An address already in use must surface as an error rather than a silent no-op.
func TestRunReportsAListenFailure(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = gh.Run(ctx, gh.Config{Addr: listener.Addr().String(), WebhookSecret: testSecret, Token: "t"}, nil)
	if err == nil {
		t.Error("Run did not report that the address was already in use")
	}
}

func TestConfigFromEnvReadsAKeyFile(t *testing.T) {
	keyPath := writePEMKey(t)

	t.Setenv(gh.EnvWebhookSecret, "a-secret")
	t.Setenv(gh.EnvToken, "")
	t.Setenv(gh.EnvAppID, "4242")
	t.Setenv(gh.EnvPrivateKey, "")
	t.Setenv(gh.EnvPrivateKeyPat, keyPath)

	cfg, err := gh.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}

	if cfg.AppID != 4242 {
		t.Errorf("AppID = %d, want 4242", cfg.AppID)
	}
	if !strings.Contains(string(cfg.PrivateKey), "PRIVATE KEY") {
		t.Error("the key file was not read")
	}
}

func TestConfigFromEnvReportsAMissingKeyFile(t *testing.T) {
	t.Setenv(gh.EnvWebhookSecret, "a-secret")
	t.Setenv(gh.EnvToken, "")
	t.Setenv(gh.EnvAppID, "1")
	t.Setenv(gh.EnvPrivateKey, "")
	t.Setenv(gh.EnvPrivateKeyPat, filepath.Join(t.TempDir(), "absent.pem"))

	if _, err := gh.ConfigFromEnv(); err == nil {
		t.Error("a missing key file was accepted")
	}
}

func TestConfigFromEnvHonoursTheAddress(t *testing.T) {
	t.Setenv(gh.EnvWebhookSecret, "a-secret")
	t.Setenv(gh.EnvToken, "a-token")
	t.Setenv(gh.EnvAddr, "127.0.0.1:9999")

	cfg, err := gh.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9999" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
}

func waitForServer(addr string) error {
	deadline := time.Now().Add(20 * time.Second)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", addr)
}
