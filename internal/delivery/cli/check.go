// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
	"github.com/VIKOIT/reversibility-engine/internal/render"
)

// checkFlags holds the parsed command line for one invocation. Keeping it a value rather than
// package state is what lets the command be run repeatedly in tests.
type checkFlags struct {
	before   []string
	format   string
	output   string
	gate     bool
	minGrade string
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
			"need in order to see what a change replaced.\n\n" +
			"Exit codes: 0 success, 1 the gate was not met, 2 the run did not complete.",
		Args: cobra.MinimumNArgs(1),
		Example: "  revctl check ./migrations\n" +
			"  revctl check --before ./k8s/base --format markdown ./k8s/head\n" +
			"  revctl check ./migrations --format json --gate",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheck(cmd, opts, flags, args)
		},
	}

	cmd.Flags().StringSliceVar(&flags.before, "before", nil,
		"path(s) holding the previous state, to compare against")
	cmd.Flags().StringVarP(&flags.format, "format", "f", render.FormatMarkdown,
		fmt.Sprintf("output format: %s", strings.Join(render.Formats(), ", ")))
	cmd.Flags().StringVarP(&flags.output, "output", "o", "",
		"write the certificate to a file instead of stdout")
	cmd.Flags().BoolVar(&flags.gate, "gate", false,
		"exit non-zero unless the grade is A; shorthand for --min-grade A, which is the setting autonomous agents must use")
	cmd.Flags().StringVar(&flags.minGrade, "min-grade", "",
		"exit non-zero if the grade is worse than this (A, B, C, or F)")

	return cmd
}

func runCheck(cmd *cobra.Command, opts Options, flags *checkFlags, paths []string) error {
	renderer, err := render.For(flags.format)
	if err != nil {
		return fmt.Errorf("selecting output format: %w", err)
	}

	minGrade, err := resolveMinGrade(flags)
	if err != nil {
		return err
	}

	// The engine is built first because the provider asks it which files are worth reading.
	// That keeps the list of interesting extensions in one place.
	eng := engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()})

	files, err := provider.NewFS(flags.before, paths, eng.Supports).
		ChangedFiles(cmd.Context(), "")
	if err != nil {
		return fmt.Errorf("reading the changeset: %w", err)
	}

	// A failing certificate is still a certificate, and it is the thing the user asked for. The
	// analysis error is reported alongside it rather than replacing it.
	cert, analysisErr := eng.Certify(cmd.Context(), files)

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

// resolveMinGrade turns the two gating flags into one threshold.
func resolveMinGrade(flags *checkFlags) (domain.Grade, error) {
	if flags.minGrade != "" {
		grade := domain.Grade(strings.ToUpper(flags.minGrade))
		if !grade.Valid() {
			return "", fmt.Errorf("invalid --min-grade %q: want A, B, C, or F", flags.minGrade)
		}
		return grade, nil
	}

	if flags.gate {
		return domain.GradeA, nil
	}

	// No gating requested. F is the floor, so nothing can fall below it.
	return "", nil
}

// applyGate compares the grade against the threshold and reports the outcome.
func applyGate(stderr io.Writer, cert domain.ReversibilityCertificate, minGrade domain.Grade) error {
	if minGrade == "" {
		return nil
	}

	if cert.Grade.Rank() >= minGrade.Rank() {
		return nil
	}

	_, _ = fmt.Fprintf(stderr, "revctl: gate failed — grade %s is below the required minimum %s\n", cert.Grade, minGrade)
	for _, blocker := range cert.Blockers {
		_, _ = fmt.Fprintf(stderr, "  - %s\n", blocker)
	}

	return errGateFailed
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
