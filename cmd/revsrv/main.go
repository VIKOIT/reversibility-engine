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

	"github.com/abdo-s1/reversibility-engine/internal/delivery/github"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := github.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "revsrv: %v\n", err)
		os.Exit(2)
	}

	// Analysis continues after the HTTP response, so shutdown is given a chance to finish work
	// GitHub already considers delivered and will not send again.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := github.Run(ctx, cfg, log); err != nil {
		fmt.Fprintf(os.Stderr, "revsrv: %v\n", err)
		os.Exit(1)
	}
}
