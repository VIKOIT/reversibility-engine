// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Exit codes. They are part of the CLI's contract with CI systems, so they are named here rather
// than scattered as literals.
const (
	// ExitOK means the run completed and any requested gate was satisfied.
	ExitOK = 0

	// ExitGateFailed means the run completed and the grade did not meet the required minimum.
	// It is distinct from ExitError so a pipeline can tell "the change is unsafe" from "the
	// tool broke" — conflating those is how a broken tool ends up ignored.
	ExitGateFailed = 1

	// ExitError means the run did not complete.
	ExitError = 2
)

// errGateFailed signals a failed gate up through cobra without printing a usage message.
var errGateFailed = errors.New("reversibility gate failed")

// errNoCommand signals a bare invocation. It is an ExitError rather than an ExitOK because a run
// that analyzed nothing must never be reported as a run that found nothing wrong.
var errNoCommand = errors.New("no command given: nothing was analyzed, which is not a pass")

// errNotAssessed signals that a gate was asked for over content no analyzer could read.
//
// It is an ExitError, not an ExitGateFailed, and the distinction is the point. ExitGateFailed
// means the engine measured the change and the measurement was too low; this means there was no
// measurement. Reporting it as a failed gate would invite the same fix a failed gate invites —
// lower the threshold — and no threshold makes an unread migration safe.
var errNotAssessed = errors.New(
	"the changeset was not assessed: it holds files that may be migrations and no analyzer supports them")

// errIncompleteCoverage signals --require-full-coverage over a partially analyzed changeset.
//
// ExitError rather than ExitGateFailed, for the same reason as errNotAssessed: the grade was not
// too low, part of the changeset simply was not measured. Reporting it as a failed gate would
// invite lowering the threshold, and no threshold makes an unread migration safe.
var errIncompleteCoverage = errors.New(
	"--require-full-coverage was given and part of the changeset was not analyzed")

// Options configures a CLI invocation. Streams are injected so the command tree is testable
// without touching the process's own stdout.
type Options struct {
	Stdout io.Writer
	Stderr io.Writer
	Args   []string
}

// Execute runs the command tree and returns the process exit code.
//
// It returns a code rather than calling os.Exit so that everything here stays testable; only
// main is allowed to end the process.
func Execute(opts Options) int {
	root := newRootCommand(opts)

	root.SetOut(opts.Stdout)
	root.SetErr(opts.Stderr)
	root.SetArgs(opts.Args)

	// Errors are printed once, by this function, with the context the command attached.
	root.SilenceErrors = true
	root.SilenceUsage = true

	err := root.Execute()
	switch {
	case err == nil:
		return ExitOK

	case errors.Is(err, errGateFailed):
		// The gate message has already been written as part of the report; repeating the error
		// here would bury it.
		return ExitGateFailed

	default:
		// A failed write to stderr cannot be reported anywhere better, and must not change the
		// exit code the caller relies on.
		_, _ = fmt.Fprintf(opts.Stderr, "revctl: %v\n", err)
		return ExitError
	}
}

func newRootCommand(opts Options) *cobra.Command {
	root := &cobra.Command{
		Use:   "revctl",
		Short: "Measure whether a change can be safely rolled back",
		Long: "revctl statically analyzes PostgreSQL migrations and rendered Kubernetes manifests\n" +
			"and emits a reversibility certificate: a grade, the concrete undo plan, and an explicit\n" +
			"list of what cannot be undone.\n\n" +
			"It is fail-closed. An unparseable file, an unrecognized construct, or an analysis that\n" +
			"errors all grade F. Unknown means unsafe.",

		// A bare "revctl" prints help and exits 2. The friendlier zero this used to return was a
		// fail-open, and the most dangerous kind: the one invocation that analyzes nothing was
		// also the only one that could never fail. Anything that loses its arguments — a
		// container entrypoint, a wrapper script, a CI template with an unset variable — then
		// reports success over no analysis at all. That is exactly how the v1.1.0 image turned
		// every @v1 consumer's gate into a green check; see §11e.
		//
		// Help goes to stderr here for the same reason: a caller piping stdout into a
		// certificate must not receive usage text where a verdict belongs.
		//
		// Asking for help is a different act and still succeeds. Cobra handles "--help" and the
		// "help" subcommand before RunE is reached, so neither passes through this path.
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SetOut(opts.Stderr)
			if err := cmd.Help(); err != nil {
				return fmt.Errorf("printing help: %w", err)
			}
			return errNoCommand
		},

		SilenceErrors: true,
		SilenceUsage:  true,
	}

	root.AddCommand(newCheckCommand(opts))
	root.AddCommand(newSnapshotCommand(opts))
	root.AddCommand(newCatalogCommand(opts))
	root.AddCommand(newVersionCommand())

	return root
}
