// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
	"github.com/VIKOIT/reversibility-engine/internal/snapshot/collect"
)

// snapshotFlags holds one invocation's command line.
type snapshotFlags struct {
	dsn         string
	kubeContext string
	kubeconfig  string
	environment string
	output      string
}

// collectedFields is printed in the help text and is the exhaustive list of what leaves your
// infrastructure. Users ask; "metadata only" has to be verifiable rather than merely asserted,
// and the first place somebody looks is --help.
const collectedFields = `WHAT IS COLLECTED — this list is exhaustive.

PostgreSQL (catalog and statistics views only, never a user table):
  tables    schema, name, reltuples row estimate, pg_relation_size,
            pg_total_relation_size
  indexes   schema, table, name, size, idx_scan count, statistics reset time
  columns   schema, table, name, null_frac, avg_width

Kubernetes (GET and LIST only):
  storage classes   name, reclaimPolicy
  claims            namespace, name, phase, storage class, bound capacity
  workloads         namespace, kind, name, replica count

WHAT IS NEVER COLLECTED:
  No row of user data. No column values. No Secret, ConfigMap, or environment
  variable — not even their names. No connection string, no hostname, and no
  credential of any kind. The source is identified by a hash of its own identity,
  never by anything you could connect with.

The PostgreSQL connection is opened with default_transaction_read_only=on, so no
statement this command issues can write. pg_monitor is sufficient to run it.`

func newSnapshotCommand(opts Options) *cobra.Command {
	flags := &snapshotFlags{}

	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Collect production metadata for use as analysis context",
		Long: "Collect metadata about a database or cluster and write it to a file.\n\n" +
			"The engine never connects to anything during analysis. This command produces a\n" +
			"snapshot; `revctl check --context` reads it. That is what keeps production\n" +
			"credentials out of CI, keeps a certificate byte-identical between runs, and keeps\n" +
			"the analyzers pure functions over a changeset.\n\n" +
			"Point it at a read replica. Nothing here writes, but a replica is one less thing to\n" +
			"reason about.\n\n" + collectedFields,
		Args: cobra.NoArgs,
		Example: "  revctl snapshot --dsn \"$REPLICA_DSN\" --out .reversibility/pg.json\n" +
			"  revctl snapshot --kube-context prod --out .reversibility/k8s.json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSnapshot(cmd, opts, flags)
		},
	}

	cmd.Flags().StringVar(&flags.dsn, "dsn", "",
		"PostgreSQL connection string; prefer a read replica, and prefer $PGPASSWORD or a .pgpass file over putting a password here")
	cmd.Flags().StringVar(&flags.kubeContext, "kube-context", "",
		"kubeconfig context to collect from; pass an empty --kube-context= to use the current one")
	cmd.Flags().StringVar(&flags.kubeconfig, "kubeconfig", "",
		"explicit kubeconfig path; defaults to $KUBECONFIG, then ~/.kube/config, then in-cluster credentials")
	cmd.Flags().StringVar(&flags.environment, "environment", "",
		"label recorded in the snapshot, such as prod; it appears in messages when two snapshots disagree about their source")
	cmd.Flags().StringVarP(&flags.output, "out", "o", "",
		"file to write the snapshot to (required)")

	return cmd
}

func runSnapshot(cmd *cobra.Command, opts Options, flags *snapshotFlags) error {
	if flags.output == "" {
		return errors.New("--out is required: a snapshot is a file, and the analysis reads it later")
	}

	kube := cmd.Flags().Changed("kube-context") || flags.kubeconfig != ""

	switch {
	case flags.dsn != "" && kube:
		return errors.New("--dsn collects a database and --kube-context collects a cluster; run the command twice and pass --context twice")
	case flags.dsn == "" && !kube:
		return errors.New("nothing to collect: pass --dsn for PostgreSQL or --kube-context for Kubernetes")
	}

	var (
		snap *snapshot.Snapshot
		err  error
	)

	if flags.dsn != "" {
		snap, err = collect.Postgres{Environment: flags.environment}.Collect(cmd.Context(), flags.dsn)
	} else {
		snap, err = collect.Kubernetes{
			Context:     flags.kubeContext,
			Kubeconfig:  flags.kubeconfig,
			Environment: flags.environment,
		}.Collect(cmd.Context())
	}
	if err != nil {
		return fmt.Errorf("collecting the snapshot: %w", err)
	}

	encoded, err := snapshot.Encode(snap)
	if err != nil {
		return fmt.Errorf("rendering the snapshot: %w", err)
	}

	if err := writeSnapshot(flags.output, encoded); err != nil {
		return err
	}

	summarizeSnapshot(opts, flags.output, snap)
	return nil
}

// writeSnapshot writes the file, creating its directory.
//
// 0o600: the file contains no credential by construction, but it does describe the shape of a
// production database, and that is not something to leave world-readable on a shared runner.
func writeSnapshot(path string, content []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// summarizeSnapshot reports what was written, in counts.
//
// Counts rather than contents: this is the reassurance that the command did something, and the
// file itself is the answer to what.
func summarizeSnapshot(opts Options, path string, snap *snapshot.Snapshot) {
	_, _ = fmt.Fprintf(opts.Stderr, "revctl: wrote %s (%s, collected %s)\n",
		path, snap.Kind, snap.CollectedAt.Format("2006-01-02 15:04:05Z"))

	if snap.Postgres != nil {
		_, _ = fmt.Fprintf(opts.Stderr, "  %d tables, %d indexes, %d column statistics\n",
			len(snap.Postgres.Tables), len(snap.Postgres.Indexes), len(snap.Postgres.Columns))
	}
	if snap.Kubernetes != nil {
		_, _ = fmt.Fprintf(opts.Stderr, "  %d storage classes, %d claims, %d workloads\n",
			len(snap.Kubernetes.StorageClasses), len(snap.Kubernetes.Claims), len(snap.Kubernetes.Workloads))
	}
}
