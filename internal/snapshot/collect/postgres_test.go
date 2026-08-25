// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package collect_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
	"github.com/VIKOIT/reversibility-engine/internal/snapshot/collect"
)

// dsnEnv names the environment variable holding a throwaway PostgreSQL instance.
//
// The suite is skipped without it rather than spinning a container itself: testcontainers would
// be a dependency this project has not approved, and CI already knows how to run a service
// container. .github/workflows/ci.yml sets this.
const dsnEnv = "REVCTL_TEST_POSTGRES_DSN"

// The values seeded into the throwaway database. Every one of them is the kind of thing that
// must never reach a snapshot file, and each is distinctive enough that a substring search for
// it cannot match by accident.
var sensitiveValues = []string{
	"canary-value-password-must-not-appear",
	"canary-value-api-token-must-not-appear",
	"canary-value-national-id-must-not-appear",
	"canary-value-patient-contact-must-not-appear",
	"canary-value-private-key-material-must-not-appear",
}

func requireDatabase(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; this test needs a throwaway PostgreSQL instance", dsnEnv)
	}
	return dsn
}

// seed creates a schema whose every column name, table name, and row screams "secret", so that
// a collector which leaked any of them would be caught by a plain substring search.
func seed(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to seed: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	statements := []string{
		`DROP TABLE IF EXISTS credentials`,
		`CREATE TABLE credentials (
			id             bigserial PRIMARY KEY,
			user_password  text NOT NULL,
			api_secret     text NOT NULL,
			social_security text,
			contact_email  text,
			private_key    text
		)`,
		`CREATE INDEX idx_credentials_email ON credentials (contact_email)`,
		fmt.Sprintf(`INSERT INTO credentials (user_password, api_secret, social_security, contact_email, private_key)
			VALUES ('%s', '%s', '%s', '%s', '%s')`,
			sensitiveValues[0], sensitiveValues[1], sensitiveValues[2], sensitiveValues[3], sensitiveValues[4]),
		// Repeated so the row is not a single outlier the planner might ignore, and so null_frac
		// has something to be non-trivial about.
		`INSERT INTO credentials (user_password, api_secret, social_security, contact_email, private_key)
			SELECT user_password, api_secret, NULL, contact_email, private_key FROM credentials`,
		`ANALYZE credentials`,
	}

	for _, stmt := range statements {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("seeding (%.60s): %v", stmt, err)
		}
	}
}

// THE TEST THAT MAKES "METADATA ONLY" CHECKABLE RATHER THAN MERELY CLAIMED.
//
// A database is seeded with values that would be catastrophic to leak, the collector is run
// against it, and the produced file is searched for every one of them. Reviewing the queries by
// eye is not enough: a future edit could add a column, and only this would notice.
func TestSnapshotContainsNoUserData(t *testing.T) {
	dsn := requireDatabase(t)
	ctx := context.Background()

	seed(ctx, t, dsn)

	snap, err := collect.Postgres{Environment: "test"}.Collect(ctx, dsn)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	encoded, err := snapshot.Encode(snap)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	file := string(encoded)

	for _, secret := range sensitiveValues {
		if strings.Contains(file, secret) {
			t.Errorf("the snapshot contains a seeded secret value (%.20s...). "+
				"Metadata only is the whole basis on which anybody points this at production.", secret)
		}
	}

	// The DSN itself, and anything from it. A password reaching the file would be the worst
	// possible outcome of a tool that exists to be pointed at production.
	if strings.Contains(file, dsn) {
		t.Error("the snapshot contains the connection string")
	}
	for _, marker := range []string{"password=", "postgres://", "postgresql://"} {
		if strings.Contains(strings.ToLower(file), marker) {
			t.Errorf("the snapshot contains %q, which is part of a connection string", marker)
		}
	}

	// Having proved what is absent, prove the collector actually ran. A test that only asserts
	// absence would pass on an empty file.
	if snap.Postgres == nil {
		t.Fatal("no postgres data was collected")
	}
	if !strings.Contains(file, "credentials") {
		t.Error("the seeded table is missing from the snapshot; the collector may have collected nothing")
	}
	if snap.SourceFingerprint == "" {
		t.Error("no source fingerprint was produced")
	}
}

// Metadata about the seeded schema has to be usable, not merely present: the null fraction is
// what turns SET NOT NULL from a lock warning into "this will fail".
func TestCollectorGathersTheStatisticsEnrichmentNeeds(t *testing.T) {
	dsn := requireDatabase(t)
	ctx := context.Background()

	seed(ctx, t, dsn)

	snap, err := collect.Postgres{}.Collect(ctx, dsn)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var table *snapshot.Table
	for i := range snap.Postgres.Tables {
		if snap.Postgres.Tables[i].Name == "credentials" {
			table = &snap.Postgres.Tables[i]
			break
		}
	}
	if table == nil {
		t.Fatal("the seeded table was not collected")
	}
	if table.SizeBytes <= 0 || table.TotalSizeBytes < table.SizeBytes {
		t.Errorf("implausible sizes: %+v", table)
	}

	var index bool
	for _, i := range snap.Postgres.Indexes {
		if i.Name == "idx_credentials_email" {
			index = true
		}
	}
	if !index {
		t.Error("the seeded index was not collected")
	}

	// social_security is null in exactly half the rows, which is what makes it the column a
	// SET NOT NULL would fail on.
	var nullable bool
	for _, c := range snap.Postgres.Columns {
		if c.Table == "credentials" && c.Name == "social_security" && c.NullFraction > 0 {
			nullable = true
		}
	}
	if !nullable {
		t.Error("no non-zero null fraction was collected for a column that is half null")
	}
}

// The connection is opened read-only, so a bug in this package cannot become a write to the
// database somebody pointed it at.
func TestCollectorConnectionIsReadOnly(t *testing.T) {
	dsn := requireDatabase(t)
	ctx := context.Background()

	// Collect first, to prove the read-only setting does not stop the collector working.
	if _, err := (collect.Postgres{}).Collect(ctx, dsn); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, `CREATE TABLE should_not_exist (id int)`); err == nil {
		t.Error("a write succeeded on a connection configured exactly as the collector's; " +
			"default_transaction_read_only is not doing what this design assumes")
		_, _ = conn.Exec(ctx, `DROP TABLE IF EXISTS should_not_exist`)
	}
}

// Collecting twice from an unchanged database produces the same bytes apart from the timestamp.
// A snapshot is an input to a certificate, and a certificate must not change because somebody
// re-ran the collector.
func TestCollectionIsStable(t *testing.T) {
	dsn := requireDatabase(t)
	ctx := context.Background()

	seed(ctx, t, dsn)

	fixed := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	c := collect.Postgres{Now: func() time.Time { return fixed }}

	first, err := c.Collect(ctx, dsn)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	second, err := c.Collect(ctx, dsn)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	a, err := snapshot.Encode(first)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := snapshot.Encode(second)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if string(a) != string(b) {
		t.Error("two collections of an unchanged database produced different bytes")
	}
	if first.SourceFingerprint != second.SourceFingerprint {
		t.Error("the fingerprint of one database is not stable between runs")
	}
}
