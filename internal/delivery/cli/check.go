// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/terraform"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/policy"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
	"github.com/VIKOIT/reversibility-engine/internal/render"
	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
)

// checkFlags holds the parsed command line for one invocation. Keeping it a value rather than
// package state is what lets the command be run repeatedly in tests.
type checkFlags struct {
	before   []string
	base     string
	head     string
	format   string
	output   string
	gate     bool
	minGrade string
	config   string
	noConfig bool
	context  []string
	plans    []string

	// requireFullCoverage is read nowhere and exists only so the flag still parses. Partial
	// coverage fails closed unconditionally now, so the flag it was bound to is a no-op kept
	// accepted for pipelines that still pass it — see the flag registration below, and §16.11
	// for why the deprecation notice does not come from cobra.
	requireFullCoverage bool
}

func newCheckCommand(opts Options) *cobra.Command {
	flags := &checkFlags{}

	cmd := &cobra.Command{
		Use:   "check [paths...]",
		Short: "Analyze a changeset and emit a reversibility certificate",
		Long: "Analyze PostgreSQL migrations and rendered Kubernetes manifests and emit a\n" +
			"reversibility certificate.\n\n" +
			"With only paths given, every file is treated as newly added — the shape of a migration\n" +
			"pull request. Pass --before to compare two trees, which is what the Kubernetes rules\n" +
			"need in order to see what a change replaced. Pass --base to compare two git refs\n" +
			"instead — the same comparison a pull request shows. Content is read from the refs, so\n" +
			"a dirty working tree cannot change the certificate.\n\n" +
			"Exit codes: 0 success, 1 the gate was not met, 2 the run did not complete.",
		Args: cobra.ArbitraryArgs,
		Example: "  revctl check ./migrations\n" +
			"  revctl check --before ./k8s/base --format markdown ./k8s/head\n" +
			"  revctl check --base origin/main\n" +
			"  revctl check --base v1.2.0 --head HEAD ./migrations\n" +
			"  revctl check ./migrations --format json --gate",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheck(cmd, opts, flags, args)
		},
	}

	cmd.Flags().StringSliceVar(&flags.before, "before", nil,
		"path(s) holding the previous state, to compare against")
	cmd.Flags().StringVar(&flags.base, "base", "",
		"git ref to compare against, e.g. origin/main; the comparison uses the merge base, matching pull-request semantics")
	cmd.Flags().StringVar(&flags.head, "head", "",
		"git ref holding the change; defaults to HEAD, and is only meaningful with --base")
	cmd.Flags().StringVarP(&flags.format, "format", "f", render.FormatMarkdown,
		fmt.Sprintf("output format: %s", strings.Join(render.Formats(), ", ")))
	cmd.Flags().StringVarP(&flags.output, "output", "o", "",
		"write the certificate to a file instead of stdout")
	// The old help for this flag called it "the setting autonomous agents must use", and since
	// coverage became a second axis that is actively wrong: this exit code follows the grade
	// alone and honours waivers, so it can be 0 on a changeset whose aiGateStatus is FAIL. An
	// agent must read the certificate, not the exit status.
	cmd.Flags().BoolVar(&flags.gate, "gate", false,
		"exit non-zero unless the grade is A; shorthand for --min-grade A. This is a grade threshold for your pipeline — an autonomous agent must read aiGateStatus, which never passes on a partially analyzed change")
	cmd.Flags().StringVar(&flags.minGrade, "min-grade", "",
		"exit non-zero if the grade is worse than this (A, B, C, or F)")
	cmd.Flags().StringVar(&flags.config, "config", "",
		"path to a .reversibility.yml policy file, instead of discovering one")
	cmd.Flags().BoolVar(&flags.noConfig, "no-config", false,
		"ignore any .reversibility.yml, including one that would be discovered")
	cmd.Flags().StringArrayVar(&flags.context, "context", nil,
		"production snapshot from `revctl snapshot`, repeatable; a path that does not exist is skipped, because context is an enhancement rather than a requirement")
	// Deprecated and now a no-op: full coverage is required unconditionally. The flag is kept
	// accepted rather than removed so that a pipeline carrying it keeps running — removing it
	// would turn an upgrade into an unknown-flag error, which is a worse failure than a warning
	// on a line that no longer does anything.
	//
	// **The notice is written by runCheck, to stderr, and cobra's MarkDeprecated is deliberately
	// not used.** pflag emits that warning from ParseFlags through cobra's *out* writer, which
	// is stdout — so `revctl check --format json --require-full-coverage` printed a line of
	// English ahead of the certificate and stdout stopped being parseable JSON. Keeping the flag
	// accepted so an upgrade would not become an unknown-flag error had turned it into a parse
	// error instead: the same failure wearing a different coat. MarkHidden does the half of
	// MarkDeprecated that is wanted here — keeping a dead flag out of the help — and prints
	// nothing anywhere. See §16.11, and TestNoFlagIsDeprecatedThroughCobra, which fails if
	// anyone reaches for MarkDeprecated again.
	cmd.Flags().BoolVar(&flags.requireFullCoverage, "require-full-coverage", false,
		"DEPRECATED and ignored: partial coverage always fails now. Remove this flag.")
	_ = cmd.Flags().MarkHidden("require-full-coverage")
	cmd.Flags().StringArrayVar(&flags.plans, "terraform-plan", nil,
		"analyze this file as a Terraform plan whatever it is called; repeatable. The default convention is *.tfplan.json, and this is the escape hatch for a plan named otherwise")

	return cmd
}

func runCheck(cmd *cobra.Command, opts Options, flags *checkFlags, paths []string) error {
	warnAboutDeprecatedFlags(cmd, opts.Stderr)

	renderer, err := render.For(flags.format)
	if err != nil {
		return fmt.Errorf("selecting output format: %w", err)
	}

	// The policy is resolved before anything reads a file. A configuration error must stop the
	// run rather than produce a certificate enforcing something nobody asked for.
	pol, err := resolvePolicy(opts, flags, paths)
	if err != nil {
		return err
	}

	minGrade, err := resolveMinGrade(flags, pol)
	if err != nil {
		return err
	}

	// Read here, in the transport layer, and handed to the engine as a value. The engine never
	// opens a connection — that is the whole design constraint of production context, and it is
	// enforced by an architecture test rather than by convention.
	production, err := snapshot.Load(flags.context, snapshot.Options{Now: time.Now()})
	if err != nil {
		return fmt.Errorf("reading the production context: %w", err)
	}
	if production != nil {
		for _, warning := range production.Warnings {
			_, _ = fmt.Fprintf(opts.Stderr, "revctl: %s\n", warning)
		}
	}

	// The engine is built first because the provider asks it which files are worth reading.
	// That keeps the list of interesting extensions in one place.
	analyzers, err := buildAnalyzers(flags, pol)
	if err != nil {
		return err
	}

	eng := engine.New(
		analyzers,
		engine.WithPolicy(pol),
		engine.WithContext(production),
	)

	// An ignored path is never read, so it is never classified and never returned as context
	// either. Filtering after analysis would leave the ignore list one refactor away from being
	// forgotten.
	//
	// Candidates are admitted alongside supported files, and that is not an optimisation to
	// undo. The engine can only classify a changeset it can see, and until this line existed a
	// docs-only pull request and thirteen unreadable Django migrations both arrived here as an
	// empty file list — indistinguishable, and both graded A. The engine hands these to the
	// analyzers too, which ignore anything they do not claim.
	// ignoredCandidates records what the policy excluded. The path is recorded, never the
	// content: an ignored file is still never read, which is the property that makes an ignore
	// list mean anything, and the certificate needs only to be able to name it.
	//
	// Recording happens here because here is the only place that knows. By the time the engine
	// sees a changeset, an ignored file is indistinguishable from a file that was never there.
	var (
		ignoredCandidates []string
		enumerated        []provider.Path
	)

	source, root, err := resolveProvider(flags, paths)
	if err != nil {
		return err
	}

	// The provider is resolved before `choose` is built, because `choose` makes path-keyed
	// decisions and every one of those is made in one namespace — the one this locator maps
	// into. Deciding first and locating afterwards is what left `ignore:` globs matching the
	// changeset's spelling while candidate detection used the repository's. See §16.13.
	locate := domain.NewLocator(root)

	// The matcher is the ignore list plus a record of which patterns did anything. A pattern
	// that matches nothing is dead config, and dead config in a safety tool reads as protection
	// the user does not have.
	ignores := pol.Matcher()

	// choose runs once over the complete listing, which is what lets it answer questions a
	// per-path predicate could not: "read this README because the directory it sits in also
	// holds a .sql file" needs to see the directory.
	//
	// It records two things besides its answer. The paths a policy excluded, so the certificate
	// can name them; and nothing else — the full listing goes to the engine separately, and it
	// is the listing, not this selection, that coverage is measured against.
	choose := func(listed []provider.Path) []provider.Path {
		out := make([]provider.Path, 0, len(listed))

		for _, p := range listed {
			// The policy file is not part of the changeset's subject matter. It is
			// configuration for the run, it is already accounted for in PolicyDigest, and
			// counting it against coverage would make every repository that has one fail.
			if policy.IsPolicyFile(p.Path) {
				continue
			}

			at := locate(p.Path)

			// An ignored path is excluded from the enumeration as well as from the read, and
			// that is the §16.8 ruling rather than an oversight: coverage describes what the
			// engine could not read, and this is something a human decided it should not. It is
			// reported separately, under IgnoredByPolicy, so nothing is hidden — and it closes
			// the merge gate when it is a real migration.
			if ignores.Ignores(at) {
				ignoredCandidates = append(ignoredCandidates, p.Path)
				continue
			}

			enumerated = append(enumerated, p)

			if eng.Supports(at) {
				out = append(out, p)
			}
		}
		return out
	}

	_, files, err := provider.Resolve(cmd.Context(), source, "", choose)
	if err != nil {
		return fmt.Errorf("reading the changeset: %w", err)
	}

	// A failing certificate is still a certificate, and it is the thing the user asked for. The
	// analysis error is reported alongside it rather than replacing it.
	//
	// The enumeration goes in beside the files, and that is the whole of §16.9: coverage is a
	// statement about what exists, so it is measured against what was listed rather than against
	// what happened to be read. Handing over only the files is what let a renamed migration
	// directory turn the check off.
	//
	// The root goes in beside it, and that is §16.10: the enumeration says what exists, and the
	// root says where it exists. `revctl check ./migrations` reports its files relative to
	// ./migrations, stripping exactly the segment that identifies them as migrations, and
	// without this the documented invocation reached NO_CANDIDATES and exit 0 over content that
	// exited 2 when the same files were named one directory up.
	cert, analysisErr := eng.Certify(cmd.Context(), files,
		engine.Enumerated(enumeratedPaths(enumerated)),
		engine.RootedAt(root),
		engine.DeadIgnores(ignores.Dead()),
		engine.IgnoredByPolicy(ignoredCandidates))

	// The certificate already carries these, and they are repeated on stderr because the person
	// who wrote the config is watching a terminal, not reading JSON. Dead config is protection
	// the user does not have, so it is said where they are.
	for _, warning := range cert.PolicyWarnings {
		_, _ = fmt.Fprintf(opts.Stderr, "revctl: %s\n", warning)
	}

	out, closeOut, err := openOutput(opts.Stdout, flags.output)
	if err != nil {
		return err
	}
	defer closeOut()

	if err := renderer.Render(out, cert); err != nil {
		return fmt.Errorf("writing the %s certificate: %w", flags.format, err)
	}

	if analysisErr != nil {
		// Writing to stderr cannot meaningfully fail, and failing to report a diagnostic is
		// not a reason to change the command's outcome.
		_, _ = fmt.Fprintf(opts.Stderr, "revctl: analysis reported errors, grade forced to F: %v\n", analysisErr)
	}

	return applyGate(opts.Stderr, cert, minGrade)
}

// deprecatedFlags maps a flag that no longer does anything to what its user should do instead.
//
// It is a table rather than a line of code per flag so that adding one cannot accidentally add
// it to the wrong stream: there is one writer for all of them and it is stderr.
var deprecatedFlags = map[string]string{
	"require-full-coverage": "partial coverage now always fails, so this flag is ignored; remove it",
}

// warnAboutDeprecatedFlags says on stderr that a flag no longer does anything.
//
// **Stdout carries the certificate and nothing else, in every format.** A diagnostic on stdout
// is not a cosmetic problem: under --format json it makes the output unparseable, and the
// pipeline that was going to read a grade out of it now reads a syntax error. Everything the
// engine wants to tell a human — warnings, deprecations, gate reasons, policy notices — goes to
// stderr, and that is why this is written here rather than left to cobra, which prints its own
// deprecation warnings through the out writer. See §16.11.
func warnAboutDeprecatedFlags(cmd *cobra.Command, stderr io.Writer) {
	names := make([]string, 0, len(deprecatedFlags))
	for name := range deprecatedFlags {
		names = append(names, name)
	}
	// Sorted, because two deprecated flags on one command line must produce the same two lines
	// in the same order every run.
	sort.Strings(names)

	for _, name := range names {
		if !cmd.Flags().Changed(name) {
			continue
		}
		_, _ = fmt.Fprintf(stderr, "revctl: --%s is deprecated: %s\n", name, deprecatedFlags[name])
	}
}

// resolveProvider picks the source of the changeset from the flags.
//
// The modes are mutually exclusive by construction rather than by precedence. Silently
// preferring one over another would let a user believe they had certified a comparison that
// never ran, and the comparison they think they ran is the one they would have gated on.
//
// It returns the root prefix alongside the provider because the two are one decision: the
// provider determines the namespace its paths are in, and the prefix is what puts them back into
// the repository's. git and GitHub already report repository-relative paths and so contribute
// nothing; only the filesystem provider reports relative to a directory somebody named.
// enumeratedPaths reduces a listing to the paths the engine needs for coverage.
func enumeratedPaths(listed []provider.Path) []string {
	out := make([]string, 0, len(listed))
	for _, p := range listed {
		out = append(out, p.Path)
	}
	return out
}

func resolveProvider(flags *checkFlags, paths []string) (provider.FileProvider, string, error) {
	switch {
	case flags.base != "" && len(flags.before) > 0:
		return nil, "", errors.New("--base and --before are different comparisons: --base names git refs, --before names directories; pass one, not both")

	case flags.base != "":
		// Path arguments become git pathspecs, scoping the comparison to a subtree.
		source, err := provider.NewGit(provider.GitOptions{
			Base:  flags.base,
			Head:  flags.head,
			Paths: paths,
		})
		if err != nil {
			return nil, "", fmt.Errorf("resolving the git refs: %w", err)
		}
		// git reports repository-relative paths whatever pathspec narrowed the comparison, so
		// there is nothing stripped to restore.
		return source, "", nil

	case flags.head != "":
		return nil, "", errors.New("--head names the ref holding the change and only means something alongside --base")

	case len(paths) == 0:
		return nil, "", errors.New("no paths given: pass a directory to analyze, or --base to compare two git refs")

	default:
		// The prefix is taken from the paths holding the new state. With --before the two trees
		// share one relative namespace by construction — that is what makes them comparable —
		// and the new state is the side a classification is a statement about.
		return provider.NewFS(flags.before, paths), provider.RootPrefix(paths), nil
	}
}

// resolvePolicy loads the policy file, if there is one to load.
//
// A missing policy is not an error: the tool must behave exactly as it did before policies
// existed. A policy that exists and cannot be read is a different matter entirely — the run does
// not know what it was meant to enforce, so it stops.
func resolvePolicy(opts Options, flags *checkFlags, paths []string) (*policy.Policy, error) {
	if flags.noConfig {
		if flags.config != "" {
			return nil, errors.New("--config names a policy and --no-config discards one; pass one, not both")
		}
		return nil, nil
	}

	path := flags.config
	if path == "" {
		discovered, err := policy.Discover(policySearchRoot(paths))
		if err != nil {
			return nil, fmt.Errorf("looking for %s: %w", policy.FileName, err)
		}
		if discovered == "" {
			return nil, nil
		}
		path = discovered
	}

	pol, err := policy.Load(path, time.Now())
	if err != nil {
		return nil, fmt.Errorf("reading the policy: %w", err)
	}

	_, _ = fmt.Fprintf(opts.Stderr, "revctl: using policy %s\n", pol.Source)
	return pol, nil
}

// policySearchRoot picks the directory the discovery walk starts from.
//
// The first path argument is used when it is something on disk. With --base the arguments are
// git pathspecs rather than paths, and a pathspec is not a directory to walk up from, so the
// working directory is the honest answer.
func policySearchRoot(paths []string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "."
}

// resolveMinGrade turns the gating flags and the policy into one threshold.
//
// An explicit flag beats the policy file. Someone who typed a threshold on the command line is
// making a decision about this run, and silently overriding it with a file they may not have
// noticed would be the wrong way round.
func resolveMinGrade(flags *checkFlags, pol *policy.Policy) (domain.Grade, error) {
	if flags.minGrade != "" {
		// Threshold, not Valid. N/A is a grade a certificate can carry and is not a bar anything
		// can clear: accepting it here would build a gate every run satisfies, which is the
		// exact shape of the bug this whole change exists to remove.
		grade := domain.Grade(strings.ToUpper(flags.minGrade))
		if !grade.Threshold() {
			return "", fmt.Errorf("invalid --min-grade %q: want A, B, C, or F", flags.minGrade)
		}
		return grade, nil
	}

	if flags.gate {
		return domain.GradeA, nil
	}

	if pol != nil && pol.Gate != "" {
		return pol.Gate, nil
	}

	// No gating requested. F is the floor, so nothing can fall below it.
	return "", nil
}

// applyGate compares the grade against the threshold and reports the outcome.
//
// It compares EffectiveGrade, which is Grade with waived findings set aside and equal to Grade
// whenever no policy applied. Grade itself is left alone deliberately: it says what the evidence
// says, and a waiver unblocks a pipeline without ever rewriting the measurement — or the AI
// merge gate, which follows Grade and so can never be opened by a waiver.
func applyGate(
	stderr io.Writer,
	cert domain.ReversibilityCertificate,
	minGrade domain.Grade,
) error {
	// Questions in order of how badly the run failed to inform anyone: did it read nothing, did
	// it read only some of it, and only then how the part it read graded.
	//
	// A waiver still moves the gate and never the grade — that half of the S10 pattern stands.
	// Coverage no longer belongs to it: an incomplete analysis is not a risk somebody accepted,
	// it is a measurement that was not taken, and it now fails closed in the grade itself. See
	// docs/SPECIFICATION.md §16.7 for the reversal and the argument.
	gateInForce := minGrade != ""

	// Nothing was read at all. The gate has no evidence to act on, and a gate with no evidence
	// must not open. Exit 2 rather than 1: the change is not being failed, the run is.
	if cert.Outcome == domain.OutcomeUnsupportedContent {
		_, _ = fmt.Fprintf(stderr, "revctl: reversibility was not assessed, so the gate cannot pass\n")
		for _, blocker := range cert.Blockers {
			_, _ = fmt.Fprintf(stderr, "  - %s\n", blocker)
		}
		return errNotAssessed
	}

	// Something was read and something was not. **A partial pass is a bypass**, so this is not
	// conditional on a flag and not conditional on a threshold: a changeset the engine can only
	// partly vouch for is a run that did not complete, and exit 2 is what that means.
	//
	// It sits above the `gateInForce` check deliberately. Without a gate nothing else here
	// fails, but this is not a verdict about the change — it is the engine reporting that it
	// could not finish, which is true whether or not anybody asked it to gate.
	if !cert.Coverage.Full() {
		_, _ = fmt.Fprintf(stderr, "revctl: %s\n", engine.PartialCoverageBlocker)

		unread := make([]string, 0, len(cert.UnanalyzedFiles))
		for _, u := range cert.UnanalyzedFiles {
			unread = append(unread, u.Path)
			_, _ = fmt.Fprintf(stderr, "  - %s (%s)\n", u.Path, u.Reason)
		}

		// The way forward, when the gap is a framework this engine will never parse. It is on
		// the certificate already; it is repeated here because stderr is where the person who
		// has just been blocked is looking, and a refusal they cannot act on is one they will
		// route around by removing the gate.
		for _, remedy := range engine.RenderingRemedy(unread) {
			_, _ = fmt.Fprintf(stderr, "  %s\n", remedy)
		}

		return errIncompleteCoverage
	}

	if !gateInForce {
		// Nothing was asked to be gated, so nothing is gated — the same reason grade F exits 0
		// here. A user who asked only for a report gets a report.
		return nil
	}

	// The outcome decides before the grade does. N/A has no rank, so comparing it against a
	// threshold would be meaningless — and it is the comparison a caller reaches for by reflex,
	// which is why domain.Grade.Rank puts N/A below F rather than leaving it to chance.
	if cert.Outcome == domain.OutcomeNoCandidates {
		// There was genuinely nothing to assess. The certificate says so, in a grade and a gate
		// status that cannot be mistaken for approval, and the run itself succeeded.
		return nil
	}

	effective := cert.EffectiveGrade
	if effective == "" {
		effective = cert.Grade
	}

	if effective.Rank() >= minGrade.Rank() {
		// The exit code and the AI merge gate can legitimately disagree, and when they do it is
		// said out loud, first, in one line.
		//
		// Two signals in one certificate that quietly point opposite ways is the disease this
		// project has now fixed three times: the :v1 image, the empty changeset, and the
		// certificate whose prose disclaimed under a green PASS. Nobody reads a JSON field to
		// discover that the green check they are looking at does not mean what it appears to.
		if cert.AIGateStatus != domain.GatePass {
			_, _ = fmt.Fprintf(stderr,
				"revctl: this run exits 0 and the AI merge gate is %s — %s. An autonomous agent must not merge this change.\n",
				cert.AIGateStatus, divergenceReason(cert))
		}

		if len(cert.Waived) > 0 && effective != cert.Grade {
			_, _ = fmt.Fprintf(stderr,
				"revctl: gate met at %s because %d finding(s) are waived; the change itself grades %s\n",
				effective, len(cert.Waived), cert.Grade)
		}
		return nil
	}

	_, _ = fmt.Fprintf(stderr, "revctl: gate failed — grade %s is below the required minimum %s\n", effective, minGrade)
	for _, blocker := range cert.Blockers {
		_, _ = fmt.Fprintf(stderr, "  - %s\n", blocker)
	}

	return errGateFailed
}

// divergenceReason says, in one clause, why the merge gate is closed on a run that exited 0.
//
// It names the specific cause rather than saying "see the certificate", because the whole point
// of printing this line is that the reader should not have to go and look.
func divergenceReason(cert domain.ReversibilityCertificate) string {
	switch {
	case !cert.Coverage.Full():
		return fmt.Sprintf("%d file(s) that may be migrations were not analyzed",
			len(cert.UnanalyzedFiles))
	case len(cert.IgnoredByPolicy) > 0:
		return fmt.Sprintf("%d candidate file(s) are excluded by %s",
			len(cert.IgnoredByPolicy), policy.FileName)
	case len(cert.Waived) > 0:
		return fmt.Sprintf("%d finding(s) are waived, and a waiver never opens the AI merge gate",
			len(cert.Waived))
	default:
		return fmt.Sprintf("the change grades %s", cert.Grade)
	}
}

// openOutput returns the destination for the certificate along with a close function.
//
// Writing to a file is a deliberate choice for CI, where the certificate is usually uploaded as
// an artifact and stdout is already full of build noise.
func openOutput(stdout io.Writer, path string) (io.Writer, func(), error) {
	if path == "" {
		return stdout, func() {}, nil
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("creating output file %s: %w", path, err)
	}

	return f, func() { _ = f.Close() }, nil
}

// buildAnalyzers assembles the registry for one run.
//
// The Terraform analyzer is the only one that can fail to construct: its Layer 3 overrides are a
// configuration rule, and a user who tried to weaken a classification must be told so rather
// than quietly obeyed. That is a configuration error, so it exits 2 — the run never happened,
// as distinct from a change that failed the gate.
func buildAnalyzers(flags *checkFlags, pol *policy.Policy) ([]analyzer.Analyzer, error) {
	var overrides []terraform.Override
	if pol != nil {
		for _, tt := range pol.TerraformTypes {
			overrides = append(overrides, terraform.Override{Type: tt.Type, Class: terraform.Class(tt.Class)})
		}
	}

	// --terraform-plan names a file relative to the user's shell; the engine knows the same file
	// by its path in the changeset. Those are two namespaces, and comparing them directly is
	// what the analyzer used to paper over with bidirectional suffix matching — which also
	// over-claimed, since `a/plan.json` suffix-matched `b/a/plan.json`. Resolving here, where
	// touching the filesystem is allowed, puts both sides in the decision namespace and lets the
	// comparison be exact. See §16.13.
	plans := make([]string, 0, len(flags.plans))
	for _, p := range flags.plans {
		plans = append(plans, provider.QualifyPath(p))
	}

	tf, err := terraform.New(terraform.Options{
		Overrides:      overrides,
		ExtraPlanPaths: plans,
	})
	if err != nil {
		return nil, fmt.Errorf("configuring the Terraform analyzer: %w", err)
	}

	return []analyzer.Analyzer{postgres.New(), kubernetes.New(), tf}, nil
}
