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

		// Without this, running "revctl" bare prints an error; printing help is friendlier and
		// still exits zero.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},

		SilenceErrors: true,
		SilenceUsage:  true,
	}

	root.AddCommand(newCheckCommand(opts))
	root.AddCommand(newVersionCommand())

	return root
}
