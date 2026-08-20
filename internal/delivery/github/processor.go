package github

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	gh "github.com/google/go-github/v66/github"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
	"github.com/VIKOIT/reversibility-engine/internal/render"
)

// processingTimeout bounds one analysis, including every API call it makes.
//
// Without it a stuck request would hold a goroutine and an installation token forever, and a
// pull request would silently never get its certificate.
const processingTimeout = 5 * time.Minute

// ClientFactory produces a GitHub client for one installation.
//
// It is a function rather than a stored client because a GitHub App holds a separate, expiring
// token per installation; a single long-lived client would either leak one installation's
// authority into another's repository or stop working after an hour.
type ClientFactory func(ctx context.Context, installationID int64) (*gh.Client, error)

// CertificateProcessor analyzes a pull request and reports the certificate back to it.
type CertificateProcessor struct {
	newClient ClientFactory
	engine    *engine.Engine
	renderer  render.Renderer
	log       *slog.Logger
}

// NewProcessor returns a processor wired to the production analyzers.
//
// The engine is constructed here rather than in the engine package because assembling the
// analyzer set is a delivery concern: deleting internal/delivery must not break engine tests.
func NewProcessor(newClient ClientFactory, log *slog.Logger) *CertificateProcessor {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io_Discard{}, nil))
	}

	return &CertificateProcessor{
		newClient: newClient,
		engine:    engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()}, engine.WithLogger(log)),
		renderer:  render.Markdown{},
		log:       log,
	}
}

// Process certifies one pull request and posts the result.
//
// Every failure path still produces a certificate. A rate limit, a network error, a file too
// large to fetch — none of them end in silence, because a pull request with no certificate looks
// identical to one that was never analyzed, and a reviewer cannot tell the difference.
func (p *CertificateProcessor) Process(ctx context.Context, job Job) error {
	client, err := p.newClient(ctx, job.InstallationID)
	if err != nil {
		// Without a client nothing can be posted either, so the failure can only be logged.
		return fmt.Errorf("authenticating for installation %d: %w", job.InstallationID, err)
	}

	cert, analysisErr := p.certify(ctx, client, job)

	if err := p.report(ctx, client, job, cert); err != nil {
		return fmt.Errorf("reporting on %s: %w", job.target, err)
	}

	p.log.Info("certified a pull request",
		"repository", job.Repository(),
		"pr", job.Number(),
		"grade", cert.Grade,
		"gate", cert.AIGateStatus,
		"findings", len(cert.Findings),
		"delivery", job.DeliveryID,
	)

	return analysisErr
}

// certify fetches the changeset and grades it.
//
// The certificate it returns is always valid. When the changeset cannot be fetched, the result
// is an explicit grade F naming the failure rather than a grade derived from whichever files
// happened to arrive — an incomplete diff must never yield a passing grade.
func (p *CertificateProcessor) certify(ctx context.Context, client *gh.Client, job Job) (domain.ReversibilityCertificate, error) {
	files, err := provider.NewGitHub(client, p.engine.Supports).
		ChangedFiles(ctx, provider.Ref(job.target.owner, job.target.repo, job.Base, job.Head))
	if err != nil {
		p.log.Error("could not fetch the changeset",
			"repository", job.Repository(), "pr", job.Number(), "error", err)

		return engine.UnavailableCertificate(domain.RuleProviderError, err), err
	}

	cert, err := p.engine.Certify(ctx, files)
	if err != nil {
		return cert, fmt.Errorf("certifying %s: %w", job.target, err)
	}
	return cert, nil
}

// report posts the rendered certificate, replacing this app's previous comment.
func (p *CertificateProcessor) report(ctx context.Context, client *gh.Client, job Job, cert domain.ReversibilityCertificate) error {
	var body bytes.Buffer
	if err := p.renderer.Render(&body, cert); err != nil {
		return fmt.Errorf("rendering the certificate: %w", err)
	}

	return upsertComment(ctx, issueCommentService{client: client}, job.target, body.String())
}

func sortStrings(in []string) []string {
	sort.Strings(in)
	return in
}
