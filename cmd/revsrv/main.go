// Command revsrv is the Reversibility Engine GitHub App server.
//
// It listens for pull_request webhooks, analyzes the changed files, and posts a reversibility
// certificate back to the pull request — replacing its previous comment rather than adding one.
//
// Configuration comes from the environment:
//
//	GITHUB_WEBHOOK_SECRET        required; authenticates every delivery
//	GITHUB_TOKEN                 a static token, or:
//	GITHUB_APP_ID                the App's numeric ID, with
//	GITHUB_APP_PRIVATE_KEY       its PEM private key inline, or
//	GITHUB_APP_PRIVATE_KEY_PATH  a path to read it from
//	REVSRV_ADDR                  listen address, default :8080
//
// The handler lives in internal/delivery/github. This file stays a launcher so that transport
// remains replaceable and everything above it stays testable without ending the process.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/VIKOIT/reversibility-engine/internal/delivery/github"
)

// exit codes: 2 means the server could not start, 1 means it stopped with an error.
const (
	exitStartupFailed = 2
	exitRuntimeFailed = 1
)

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "revsrv: %v\n", err)
	}
	os.Exit(code)
}

// run holds everything that needs a deferred cleanup.
//
// os.Exit does not run deferred functions, so calling it from main alongside a defer would
// silently skip the signal handler's teardown. Keeping the exit call in main and the defers
// here is the only arrangement where both actually happen.
func run() (int, error) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := github.ConfigFromEnv()
	if err != nil {
		return exitStartupFailed, err
	}

	// Analysis continues after the HTTP response, so shutdown is given a chance to finish work
	// GitHub already considers delivered and will not send again.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := github.Run(ctx, cfg, log); err != nil {
		return exitRuntimeFailed, err
	}
	return 0, nil
}
