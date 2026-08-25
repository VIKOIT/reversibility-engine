// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
)

// The queries. They are written out in full, here, rather than assembled, because "metadata
// only" is a claim a user has to be able to check — and the only way to check it is to read the
// exact SQL that runs against their database. Every column selected appears in
// docs/PRODUCTION-CONTEXT.md, and there is no SELECT anywhere in this package that touches a
// user table.
//
// System schemas are excluded: they are not what any migration in a repository alters, and
// collecting them would put catalog names in a file for no purpose.
const (
	tablesQuery = `
SELECT n.nspname                              AS schema,
       c.relname                              AS name,
       c.reltuples::bigint                    AS row_estimate,
       pg_relation_size(c.oid)                AS size_bytes,
       pg_total_relation_size(c.oid)          AS total_size_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE c.relkind IN ('r', 'p')
   AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
   AND n.nspname NOT LIKE 'pg_temp%'`

	indexesQuery = `
SELECT s.schemaname   AS schema,
       s.relname      AS table_name,
       s.indexrelname AS name,
       pg_relation_size(s.indexrelid) AS size_bytes,
       s.idx_scan     AS scans
  FROM pg_stat_user_indexes s`

	columnsQuery = `
SELECT s.schemaname AS schema,
       s.tablename  AS table_name,
       s.attname    AS name,
       s.null_frac  AS null_fraction,
       s.avg_width  AS average_width
  FROM pg_stats s
 WHERE s.schemaname NOT IN ('pg_catalog', 'information_schema')`

	// statsResetQuery reports when the counters behind idx_scan were last cleared. A zero scan
	// count means nothing without it.
	statsResetQuery = `SELECT stats_reset FROM pg_stat_database WHERE datname = current_database()`

	// identityQuery is what the fingerprint is built from. The system identifier is generated
	// at initdb and is stable across restarts, restores, and hostname changes; the database
	// name distinguishes two databases in one cluster. Neither is a credential and neither
	// tells anybody how to reach the server.
	identityQuery = `SELECT system_identifier::text, current_database() FROM pg_control_system()`

	// identityFallbackQuery is for a managed service that revokes pg_control_system(), which
	// several of them do. It is weaker — a restored copy shares its identity with the original
	// — and that is acceptable for a fingerprint whose job is to catch "these two files are
	// from different places".
	identityFallbackQuery = `SELECT current_setting('cluster_name', true), current_database()`
)

// Postgres collects metadata from a PostgreSQL database.
//
// It never reads a row of user data. Every query above is against a catalog or statistics view,
// and the connection is opened read-only so that a bug here cannot become a write.
type Postgres struct {
	// Environment labels the source in the resulting file, such as "prod".
	Environment string

	// Now is the clock, injected so a collector run is reproducible in tests.
	Now func() time.Time
}

// Collect connects, reads metadata, and returns a canonical snapshot.
//
// The DSN is used and discarded. It is never stored, never logged, and never hashed into the
// fingerprint: it carries a password, and a file that leaks production credentials would be a
// far worse outcome than having no context at all.
func (p Postgres) Collect(ctx context.Context, dsn string) (*snapshot.Snapshot, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// The DSN is deliberately absent from this message. A parse error commonly quotes the
		// input back, and the input contains a password.
		return nil, fmt.Errorf("the connection string could not be parsed: %w", redactDSN(err))
	}

	// Read-only by construction rather than by discipline. Every statement on this connection
	// runs in a read-only transaction, so no future edit to this file — and no bug in it — can
	// write to the database somebody pointed it at.
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["application_name"] = "reversibility-engine-snapshot"

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", redactDSN(err))
	}
	defer func() { _ = conn.Close(ctx) }()

	now := time.Now
	if p.Now != nil {
		now = p.Now
	}

	fingerprint, err := p.fingerprint(ctx, conn)
	if err != nil {
		return nil, err
	}

	data := &snapshot.PostgresData{}

	if data.Tables, err = collectTables(ctx, conn); err != nil {
		return nil, err
	}
	if data.Indexes, err = collectIndexes(ctx, conn); err != nil {
		return nil, err
	}
	if data.Columns, err = collectColumns(ctx, conn); err != nil {
		return nil, err
	}

	snap := &snapshot.Snapshot{
		SchemaVersion:     snapshot.SchemaVersion,
		Kind:              snapshot.KindPostgres,
		Environment:       p.Environment,
		CollectedAt:       now().UTC().Truncate(time.Second),
		SourceFingerprint: fingerprint,
		Postgres:          data,
	}
	snap.Canonicalize()
	return snap, nil
}

// fingerprint identifies the database without describing how to reach it.
func (p Postgres) fingerprint(ctx context.Context, conn *pgx.Conn) (string, error) {
	var identity, database string

	err := conn.QueryRow(ctx, identityQuery).Scan(&identity, &database)
	if err != nil {
		// Managed services frequently revoke pg_control_system(). Falling back keeps the
		// collector usable there rather than refusing to run at all.
		var clusterName *string
		if fallbackErr := conn.QueryRow(ctx, identityFallbackQuery).Scan(&clusterName, &database); fallbackErr != nil {
			return "", fmt.Errorf("reading the database identity: %w", redactDSN(err))
		}
		if clusterName != nil {
			identity = *clusterName
		}
	}

	// Hashed rather than stored. The system identifier is not a secret, but a fingerprint only
	// has to be comparable, and a hash cannot be mistaken for something to connect with.
	sum := sha256.Sum256([]byte("postgres\x00" + identity + "\x00" + database))
	return hex.EncodeToString(sum[:]), nil
}

func collectTables(ctx context.Context, conn *pgx.Conn) ([]snapshot.Table, error) {
	rows, err := conn.Query(ctx, tablesQuery)
	if err != nil {
		return nil, fmt.Errorf("reading table sizes: %w", redactDSN(err))
	}
	defer rows.Close()

	var out []snapshot.Table
	for rows.Next() {
		var t snapshot.Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.RowEstimate, &t.SizeBytes, &t.TotalSizeBytes); err != nil {
			return nil, fmt.Errorf("reading a table row: %w", redactDSN(err))
		}
		out = append(out, t)
	}
	return out, rowsErr(rows.Err(), "table sizes")
}

func collectIndexes(ctx context.Context, conn *pgx.Conn) ([]snapshot.Index, error) {
	// One query for the reset time, applied to every index: the counters behind idx_scan are
	// per-database, so the answer is the same for all of them.
	var resetAt *time.Time
	if err := conn.QueryRow(ctx, statsResetQuery).Scan(&resetAt); err != nil {
		// Not fatal. A missing reset time makes a zero scan count less trustworthy, which the
		// enrichment says out loud rather than hiding.
		resetAt = nil
	}

	rows, err := conn.Query(ctx, indexesQuery)
	if err != nil {
		return nil, fmt.Errorf("reading index statistics: %w", redactDSN(err))
	}
	defer rows.Close()

	var out []snapshot.Index
	for rows.Next() {
		var i snapshot.Index
		if err := rows.Scan(&i.Schema, &i.Table, &i.Name, &i.SizeBytes, &i.Scans); err != nil {
			return nil, fmt.Errorf("reading an index row: %w", redactDSN(err))
		}
		if resetAt != nil {
			utc := resetAt.UTC()
			i.StatsResetAt = &utc
		}
		out = append(out, i)
	}
	return out, rowsErr(rows.Err(), "index statistics")
}

func collectColumns(ctx context.Context, conn *pgx.Conn) ([]snapshot.Column, error) {
	rows, err := conn.Query(ctx, columnsQuery)
	if err != nil {
		return nil, fmt.Errorf("reading column statistics: %w", redactDSN(err))
	}
	defer rows.Close()

	var out []snapshot.Column
	for rows.Next() {
		var c snapshot.Column
		if err := rows.Scan(&c.Schema, &c.Table, &c.Name, &c.NullFraction, &c.AverageWidth); err != nil {
			return nil, fmt.Errorf("reading a column row: %w", redactDSN(err))
		}
		out = append(out, c)
	}
	return out, rowsErr(rows.Err(), "column statistics")
}

func rowsErr(err error, what string) error {
	if err != nil {
		return fmt.Errorf("reading %s: %w", what, redactDSN(err))
	}
	return nil
}
