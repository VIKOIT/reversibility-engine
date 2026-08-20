// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	gh "github.com/VIKOIT/reversibility-engine/internal/delivery/github"
)

// prEvent builds a pull_request payload.
func prEvent(action string, override func(map[string]any)) string {
	payload := map[string]any{
		"action": action,
		"number": 42,
		"pull_request": map[string]any{
			"number": 42,
			"draft":  false,
			"base":   map[string]any{"sha": "basesha"},
			"head":   map[string]any{"sha": "headsha"},
		},
		"repository": map[string]any{
			"name":  "widgets",
			"owner": map[string]any{"login": "acme"},
		},
		"installation": map[string]any{"id": int64(1234)},
	}

	if override != nil {
		override(payload)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestAnalyzedActionsAreDispatched(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"opened", "reopened", "synchronize", "ready_for_review"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			body := prEvent(action, nil)
			processor := &recordingProcessor{}

			rec := deliver(t, newHandler(t, processor), "pull_request", sign(body), body)

			if rec.Code != http.StatusAccepted {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusAccepted)
			}
			if len(processor.jobs) != 1 {
				t.Fatalf("got %d jobs, want 1", len(processor.jobs))
			}

			job := processor.jobs[0]
			if job.Repository() != "acme/widgets" || job.Number() != 42 {
				t.Errorf("job targets %s#%d, want acme/widgets#42", job.Repository(), job.Number())
			}
			if job.Base != "basesha" || job.Head != "headsha" {
				t.Errorf("job compares %s...%s, want basesha...headsha", job.Base, job.Head)
			}
			if job.InstallationID != 1234 {
				t.Errorf("InstallationID = %d, want 1234", job.InstallationID)
			}
		})
	}
}

// Re-analyzing on an action that cannot change the diff burns API budget to reach the same
// answer, and republishes a comment nobody asked for.
func TestIrrelevantActionsAreIgnored(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"labeled", "unlabeled", "assigned", "closed", "edited", "review_requested"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			body := prEvent(action, nil)
			processor := &recordingProcessor{}

			rec := deliver(t, newHandler(t, processor), "pull_request", sign(body), body)

			// Acknowledged, so GitHub stops retrying, but not acted on.
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if len(processor.jobs) != 0 {
				t.Errorf("action %q was dispatched", action)
			}
		})
	}
}

func TestPingIsAcknowledged(t *testing.T) {
	t.Parallel()

	body := `{"zen":"Design for failure."}`
	processor := &recordingProcessor{}

	rec := deliver(t, newHandler(t, processor), "ping", sign(body), body)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(processor.jobs) != 0 {
		t.Error("a ping was dispatched for analysis")
	}
}

// An event the app is not subscribed to is acknowledged rather than errored, so GitHub stops
// retrying a delivery nothing will ever act on.
func TestUnsubscribedEventsAreAcknowledged(t *testing.T) {
	t.Parallel()

	for _, event := range []string{"push", "issues", "release", "workflow_run"} {
		body := `{"action":"opened"}`
		processor := &recordingProcessor{}

		rec := deliver(t, newHandler(t, processor), event, sign(body), body)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", event, rec.Code, http.StatusOK)
		}
		if len(processor.jobs) != 0 {
			t.Errorf("%s was dispatched", event)
		}
	}
}

// Comparing the wrong commits would certify a change nobody made, so an incomplete payload is
// refused rather than guessed at.
func TestIncompletePayloadsAreRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		override func(map[string]any)
	}{
		{"no base commit", func(p map[string]any) {
			p["pull_request"].(map[string]any)["base"] = map[string]any{"sha": ""}
		}},
		{"no head commit", func(p map[string]any) {
			p["pull_request"].(map[string]any)["head"] = map[string]any{"sha": ""}
		}},
		{"no repository owner", func(p map[string]any) {
			p["repository"].(map[string]any)["owner"] = map[string]any{"login": ""}
		}},
		{"no repository name", func(p map[string]any) {
			p["repository"].(map[string]any)["name"] = ""
		}},
		{"no pull request number", func(p map[string]any) {
			p["number"] = 0
			p["pull_request"].(map[string]any)["number"] = 0
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := prEvent("opened", tt.override)
			processor := &recordingProcessor{}

			rec := deliver(t, newHandler(t, processor), "pull_request", sign(body), body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if len(processor.jobs) != 0 {
				t.Errorf("an incomplete payload was dispatched: %+v", processor.jobs[0])
			}
		})
	}
}

// A payload that is authentic but unparseable will not parse on retry either.
func TestMalformedAuthenticPayloadIsRefused(t *testing.T) {
	t.Parallel()

	body := `{"action": "opened", "pull_request": [broken`
	processor := &recordingProcessor{}

	rec := deliver(t, newHandler(t, processor), "pull_request", sign(body), body)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if len(processor.jobs) != 0 {
		t.Error("a malformed payload was dispatched")
	}
}

// A processor failure is a server-side problem, not the caller's. GitHub already has its 202 and
// the failure is only actionable in the logs.
func TestProcessorFailureDoesNotChangeTheResponse(t *testing.T) {
	t.Parallel()

	body := prEvent("opened", nil)
	processor := &recordingProcessor{err: fmt.Errorf("rate limited")}

	rec := deliver(t, newHandler(t, processor), "pull_request", sign(body), body)

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if len(processor.jobs) != 1 {
		t.Errorf("the job was not dispatched despite the processor failing")
	}
}

// The pull request number may arrive at either level of the payload.
func TestNumberIsReadFromEitherField(t *testing.T) {
	t.Parallel()

	body := prEvent("opened", func(p map[string]any) {
		p["number"] = 99
		p["pull_request"].(map[string]any)["number"] = 0
	})

	processor := &recordingProcessor{}
	deliver(t, newHandler(t, processor), "pull_request", sign(body), body)

	if len(processor.jobs) != 1 || processor.jobs[0].Number() != 99 {
		t.Errorf("number was not read from the top-level field: %+v", processor.jobs)
	}
}

func TestConfigFromEnvRequiresASecret(t *testing.T) {
	t.Setenv(gh.EnvWebhookSecret, "")
	t.Setenv(gh.EnvToken, "a-token")

	if _, err := gh.ConfigFromEnv(); err == nil {
		t.Error("a server with no webhook secret was allowed to start")
	}
}

// A server that authenticates every delivery correctly and then cannot say anything about it is
// worse than no gate: the pull request looks reviewed.
func TestConfigFromEnvRequiresCredentials(t *testing.T) {
	t.Setenv(gh.EnvWebhookSecret, "a-secret")
	t.Setenv(gh.EnvToken, "")
	t.Setenv(gh.EnvAppID, "")
	t.Setenv(gh.EnvPrivateKey, "")

	if _, err := gh.ConfigFromEnv(); err == nil {
		t.Error("a server with no credentials was allowed to start")
	}
}

func TestConfigFromEnvAcceptsAToken(t *testing.T) {
	t.Setenv(gh.EnvWebhookSecret, "a-secret")
	t.Setenv(gh.EnvToken, "a-token")
	t.Setenv(gh.EnvAppID, "")
	t.Setenv(gh.EnvPrivateKey, "")

	cfg, err := gh.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Addr != gh.DefaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, gh.DefaultAddr)
	}
	if string(cfg.WebhookSecret) != "a-secret" {
		t.Errorf("WebhookSecret = %q", cfg.WebhookSecret)
	}
}

func TestConfigFromEnvRejectsANonNumericAppID(t *testing.T) {
	t.Setenv(gh.EnvWebhookSecret, "a-secret")
	t.Setenv(gh.EnvToken, "")
	t.Setenv(gh.EnvAppID, "not-a-number")
	t.Setenv(gh.EnvPrivateKey, "key")

	if _, err := gh.ConfigFromEnv(); err == nil {
		t.Error("a non-numeric app ID was accepted")
	}
}
