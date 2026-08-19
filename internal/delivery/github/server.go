package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Config is the server's runtime configuration, read from the environment.
type Config struct {
	// Addr is the listen address, such as ":8080".
	Addr string

	// WebhookSecret authenticates deliveries. There is no default and no way to disable it.
	WebhookSecret []byte

	// Token authenticates API calls with a single static credential.
	Token string

	// AppID and PrivateKey authenticate as a GitHub App, minting a token per installation.
	AppID      int64
	PrivateKey []byte
}

// Environment variables the server reads.
const (
	EnvAddr          = "REVSRV_ADDR"
	EnvWebhookSecret = "GITHUB_WEBHOOK_SECRET"
	EnvToken         = "GITHUB_TOKEN"
	EnvAppID         = "GITHUB_APP_ID"
	EnvPrivateKey    = "GITHUB_APP_PRIVATE_KEY"
	EnvPrivateKeyPat = "GITHUB_APP_PRIVATE_KEY_PATH"
)

// DefaultAddr is used when REVSRV_ADDR is unset.
const DefaultAddr = ":8080"

// ConfigFromEnv reads configuration from the process environment.
//
// It refuses to start without a webhook secret and without credentials. Both are refusals rather
// than warnings: a server with no secret would accept anyone's payload, and one with no
// credentials would authenticate every delivery correctly and then be unable to say anything
// about it — a gate that silently reports nothing is worse than no gate, because the pull request
// looks reviewed.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Addr:          valueOr(os.Getenv(EnvAddr), DefaultAddr),
		WebhookSecret: []byte(os.Getenv(EnvWebhookSecret)),
		Token:         os.Getenv(EnvToken),
		PrivateKey:    []byte(os.Getenv(EnvPrivateKey)),
	}

	if len(cfg.WebhookSecret) == 0 {
		return Config{}, fmt.Errorf("%s is required: without it the server cannot tell a GitHub delivery from a stranger's", EnvWebhookSecret)
	}

	if raw := os.Getenv(EnvAppID); raw != "" {
		appID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be a number, got %q", EnvAppID, raw)
		}
		cfg.AppID = appID
	}

	// The key is accepted inline or as a path, because a PEM file is awkward to pass through
	// some orchestrators as an environment variable and awkward to mount in others.
	if path := os.Getenv(EnvPrivateKeyPat); path != "" && len(cfg.PrivateKey) == 0 {
		key, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("reading %s from %s: %w", EnvPrivateKey, path, err)
		}
		cfg.PrivateKey = key
	}

	if cfg.Token == "" && (cfg.AppID == 0 || len(cfg.PrivateKey) == 0) {
		return Config{}, fmt.Errorf("credentials are required: set %s, or both %s and %s (or %s)",
			EnvToken, EnvAppID, EnvPrivateKey, EnvPrivateKeyPat)
	}

	return cfg, nil
}

// clientFactory chooses the authentication strategy the configuration describes.
//
// App credentials win when both are present: they are installation-scoped and short-lived, so
// they are the safer of the two, and a deployment that has configured both almost certainly
// means the App.
func (c Config) clientFactory() (ClientFactory, string, error) {
	if c.AppID != 0 && len(c.PrivateKey) > 0 {
		authenticator, err := NewAppAuthenticator(c.AppID, c.PrivateKey)
		if err != nil {
			return nil, "", err
		}
		return authenticator.ClientFactory(), fmt.Sprintf("GitHub App %d", c.AppID), nil
	}

	if c.Token != "" {
		return StaticTokenFactory(c.Token), "static token", nil
	}

	return nil, "", errors.New("no GitHub credentials configured")
}

// NewServer builds the HTTP server for the given configuration.
func NewServer(cfg Config, log *slog.Logger) (*http.Server, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	factory, authDescription, err := cfg.clientFactory()
	if err != nil {
		return nil, err
	}

	handler := NewHandler(cfg.WebhookSecret, NewProcessor(factory, log), WithLogger(log))

	mux := http.NewServeMux()
	mux.Handle("POST /webhook", handler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	log.Info("starting the reversibility webhook server",
		"addr", cfg.Addr, "auth", authDescription, "events", eventNames())

	return &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,

		// A webhook body is small and GitHub is not slow. These bounds exist so an
		// unauthenticated client cannot hold a connection open indefinitely, which is the
		// cheapest denial of service there is against a server that must stay reachable.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,

		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError),
	}, nil
}

// Run starts the server and shuts it down cleanly when ctx is cancelled.
//
// The graceful shutdown matters more here than in most servers: analysis runs after the HTTP
// response, so an abrupt exit would drop certificates for pull requests GitHub already considers
// delivered and will not retry.
func Run(ctx context.Context, cfg Config, log *slog.Logger) error {
	server, err := NewServer(cfg, log)
	if err != nil {
		return err
	}

	errs := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err

	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), processingTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutting down: %w", err)
		}
		return nil
	}
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
