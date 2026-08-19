package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Headers GitHub sends with every delivery.
const (
	eventHeader    = "X-GitHub-Event"
	deliveryHeader = "X-GitHub-Delivery"
)

// The pull_request actions worth analyzing. Anything else — a label change, an assignment, a
// comment — cannot alter the diff, so re-running would burn API budget to reach the same answer.
var analyzedActions = map[string]bool{
	"opened":           true,
	"reopened":         true,
	"synchronize":      true,
	"ready_for_review": true,
}

// pullRequestEvent is the subset of the webhook payload this server reads.
//
// It is decoded into a local struct rather than go-github's full event type so that the fields
// the server depends on are visible in one place, and so a change in the upstream library cannot
// quietly alter which commits get compared.
type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number int  `json:"number"`
		Draft  bool `json:"draft"`
		Base   struct {
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// pullRequestTarget identifies the pull request a certificate belongs to.
type pullRequestTarget struct {
	owner  string
	repo   string
	number int
}

func (t pullRequestTarget) String() string {
	return fmt.Sprintf("%s/%s#%d", t.owner, t.repo, t.number)
}

// Handler serves GitHub webhook deliveries.
//
// Nothing is parsed, logged, or acted on before the signature is verified. An unauthenticated
// request is a stranger's bytes, and this server treats them as such.
type Handler struct {
	secret     []byte
	processor  Processor
	dispatcher dispatcher
	log        *slog.Logger
}

// Processor analyzes one pull request and reports the result back to GitHub.
//
// It is an interface so the HTTP layer can be tested — signature handling, event routing, status
// codes — without a GitHub client or a network.
type Processor interface {
	Process(ctx context.Context, job Job) error
}

// Job is everything the processor needs to certify one pull request.
type Job struct {
	target         pullRequestTarget
	Base           string
	Head           string
	InstallationID int64
	DeliveryID     string
}

// Repository returns "owner/repo" for logging.
func (j Job) Repository() string { return j.target.owner + "/" + j.target.repo }

// Number returns the pull request number.
func (j Job) Number() int { return j.target.number }

// dispatcher decides whether analysis runs before or after the HTTP response.
type dispatcher interface {
	dispatch(job func())
}

// NewHandler returns a webhook handler.
func NewHandler(secret []byte, processor Processor, opts ...HandlerOption) *Handler {
	h := &Handler{
		secret:     secret,
		processor:  processor,
		dispatcher: asyncDispatcher{},
		log:        slog.New(slog.NewTextHandler(io_Discard{}, nil)),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// HandlerOption configures a Handler.
type HandlerOption func(*Handler)

// WithLogger sets the logger.
func WithLogger(log *slog.Logger) HandlerOption {
	return func(h *Handler) {
		if log != nil {
			h.log = log
		}
	}
}

// WithSynchronousProcessing runs analysis before the HTTP response is written.
//
// The default is asynchronous, because GitHub abandons a delivery that takes longer than about
// ten seconds and a repository with many migrations can exceed that. Synchronous processing
// exists for tests, where a background goroutine would make every assertion a race.
func WithSynchronousProcessing() HandlerOption {
	return func(h *Handler) { h.dispatcher = syncDispatcher{} }
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "only POST is accepted", http.StatusMethodNotAllowed)
		return
	}

	// Authentication first. Nothing below this line runs for a request that cannot prove it
	// came from GitHub.
	body, err := verifyPayload(r, h.secret)
	if err != nil {
		h.rejectUnauthenticated(w, r, err)
		return
	}

	event := r.Header.Get(eventHeader)
	delivery := r.Header.Get(deliveryHeader)

	switch event {
	case "ping":
		h.respond(w, http.StatusOK, "pong")
		return

	case "pull_request":
		h.handlePullRequest(w, r, body, delivery)
		return

	default:
		// An event the app is not subscribed to is not an error; acknowledging it stops GitHub
		// retrying a delivery nothing will ever act on.
		h.respond(w, http.StatusOK, fmt.Sprintf("event %q ignored", event))
		return
	}
}

// rejectUnauthenticated answers a request that failed verification.
//
// The response is deliberately uniform. A caller learns that verification failed and nothing
// else — not whether the header was missing, malformed, or simply wrong — because each of those
// distinctions is a hint toward a working forgery.
func (h *Handler) rejectUnauthenticated(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusUnauthorized
	if errors.Is(err, ErrInvalidSignature) {
		status = http.StatusForbidden
	}

	h.log.Warn("rejected an unauthenticated webhook delivery",
		"reason", err,
		"remote", r.RemoteAddr,
		"delivery", r.Header.Get(deliveryHeader),
	)

	http.Error(w, "signature verification failed", status)
}

func (h *Handler) handlePullRequest(w http.ResponseWriter, r *http.Request, body []byte, delivery string) {
	var event pullRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		// The payload is authentic but unreadable. Retrying will not help, so this is a 400
		// rather than a 500.
		h.log.Error("authenticated payload did not parse", "delivery", delivery, "error", err)
		http.Error(w, "malformed pull_request payload", http.StatusBadRequest)
		return
	}

	if !analyzedActions[event.Action] {
		h.respond(w, http.StatusOK, fmt.Sprintf("action %q ignored", event.Action))
		return
	}

	job, err := event.job(delivery)
	if err != nil {
		h.log.Error("authenticated payload was incomplete", "delivery", delivery, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.dispatcher.dispatch(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), processingTimeout)
		defer cancel()

		if err := h.processor.Process(ctx, job); err != nil {
			// The certificate itself is reported to the pull request by the processor. An error
			// reaching here means even that failed, which is only actionable in the logs.
			h.log.Error("processing failed",
				"delivery", delivery, "repository", job.Repository(), "pr", job.Number(), "error", err)
		}
	})

	h.respond(w, http.StatusAccepted, "accepted")
}

// job validates the parts of the payload the analysis depends on.
//
// A missing base or head SHA is not something to work around: comparing the wrong commits would
// produce a certificate for a change nobody made.
func (e pullRequestEvent) job(delivery string) (Job, error) {
	number := e.PullRequest.Number
	if number == 0 {
		number = e.Number
	}

	target := pullRequestTarget{
		owner:  e.Repository.Owner.Login,
		repo:   e.Repository.Name,
		number: number,
	}

	switch {
	case target.owner == "" || target.repo == "":
		return Job{}, errors.New("payload names no repository")
	case target.number == 0:
		return Job{}, errors.New("payload names no pull request number")
	case e.PullRequest.Base.SHA == "":
		return Job{}, errors.New("payload carries no base commit")
	case e.PullRequest.Head.SHA == "":
		return Job{}, errors.New("payload carries no head commit")
	}

	return Job{
		target:         target,
		Base:           e.PullRequest.Base.SHA,
		Head:           e.PullRequest.Head.SHA,
		InstallationID: e.Installation.ID,
		DeliveryID:     delivery,
	}, nil
}

func (h *Handler) respond(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message + "\n"))
}

// asyncDispatcher runs analysis after the response, which is what keeps GitHub from timing out
// the delivery and retrying work already in flight.
type asyncDispatcher struct{}

func (asyncDispatcher) dispatch(job func()) { go job() }

// syncDispatcher runs analysis before the response.
type syncDispatcher struct{}

func (syncDispatcher) dispatch(job func()) { job() }

// io_Discard is a no-op writer for the default logger, so a handler constructed without
// WithLogger stays silent rather than writing to stderr from a library.
type io_Discard struct{}

func (io_Discard) Write(p []byte) (int, error) { return len(p), nil }

// eventNames returns the events this handler acts on, for the startup banner.
func eventNames() string {
	names := make([]string, 0, len(analyzedActions))
	for action := range analyzedActions {
		names = append(names, action)
	}
	return "pull_request: " + strings.Join(sortStrings(names), ", ")
}
