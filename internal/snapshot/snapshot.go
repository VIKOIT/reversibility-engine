// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package snapshot

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the version of the snapshot file format.
//
// It is separate from the certificate's schema version because the two move for different
// reasons: this one changes when what is collected changes, and a consumer must be able to
// refuse a file it cannot read rather than half-read it.
const SchemaVersion = "1.0.0"

// Kind names what a snapshot describes. Files are merged by kind, so one run can be given both
// a database and a cluster.
type Kind string

// The complete set of snapshot kinds.
const (
	KindPostgres   Kind = "postgres"
	KindKubernetes Kind = "kubernetes"
)

// Valid reports whether k is a defined kind. An unrecognised kind is refused rather than
// ignored: a file the engine cannot interpret must not be silently treated as no file at all.
func (k Kind) Valid() bool {
	return k == KindPostgres || k == KindKubernetes
}

// Snapshot is one collected view of a production environment.
//
// It contains METADATA ONLY. No row, no column value, no secret, and no connection string ever
// enters this structure — see docs/PRODUCTION-CONTEXT.md for the exhaustive list of what is
// collected and the reasoning for why nothing else is.
type Snapshot struct {
	SchemaVersion string `json:"schemaVersion"`

	Kind Kind `json:"kind"`

	// Environment is a caller-supplied label such as "prod". It exists so that a fingerprint
	// change can be reported against something a human recognises.
	Environment string `json:"environment,omitempty"`

	// CollectedAt is when the snapshot was taken, in UTC. It is the one value in this file that
	// is expected to differ between runs, and it is why a snapshot is never hashed into a
	// certificate by its bytes alone.
	CollectedAt time.Time `json:"collectedAt"`

	// SourceFingerprint identifies the database or cluster this came from, WITHOUT identifying
	// how to reach it. It is a hash of the source's own identity — never the DSN, which carries
	// a password, and never a hostname, which is an access hint.
	SourceFingerprint string `json:"sourceFingerprint"`

	Postgres   *PostgresData   `json:"postgres,omitempty"`
	Kubernetes *KubernetesData `json:"kubernetes,omitempty"`
}

// PostgresData is the metadata collected from a PostgreSQL instance.
type PostgresData struct {
	Tables  []Table  `json:"tables"`
	Indexes []Index  `json:"indexes"`
	Columns []Column `json:"columns"`
}

// Table is one relation's size, as the planner sees it.
type Table struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`

	// RowEstimate is pg_class.reltuples: the planner's estimate, updated by ANALYZE and VACUUM
	// rather than continuously. -1 means the relation has never been analyzed.
	RowEstimate int64 `json:"rowEstimate"`

	// SizeBytes is pg_relation_size: the main fork only.
	SizeBytes int64 `json:"sizeBytes"`

	// TotalSizeBytes is pg_total_relation_size: the table plus its indexes and TOAST.
	TotalSizeBytes int64 `json:"totalSizeBytes"`
}

// Index is one index's size and how often the planner has chosen it.
type Index struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Name   string `json:"name"`

	SizeBytes int64 `json:"sizeBytes"`

	// Scans is pg_stat_user_indexes.idx_scan, the count since statistics were last reset.
	// Zero is meaningful — an index nothing has read is genuinely cheap to drop — but only if
	// the counter has been running long enough, which is why the reset time travels with it.
	Scans int64 `json:"scans"`

	// StatsResetAt is when the statistics counters were last reset, if the server knows. A zero
	// scan count since five minutes ago says nothing at all.
	StatsResetAt *time.Time `json:"statsResetAt,omitempty"`
}

// Column is the per-column statistic the rules actually need.
type Column struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Name   string `json:"name"`

	// NullFraction is pg_stats.null_frac: the estimated proportion of rows where this column is
	// null. It is what makes "SET NOT NULL will fail" answerable before it fails.
	NullFraction float64 `json:"nullFraction"`

	// AverageWidth is pg_stats.avg_width, in bytes.
	AverageWidth int `json:"averageWidth"`
}

// KubernetesData is the metadata collected from a cluster.
type KubernetesData struct {
	StorageClasses []StorageClass `json:"storageClasses"`
	Claims         []Claim        `json:"claims"`
	Workloads      []Workload     `json:"workloads"`
}

// StorageClass carries the one field that decides whether deleting a claim destroys the volume.
type StorageClass struct {
	Name string `json:"name"`

	// ReclaimPolicy is Delete or Retain. Static analysis has to guess this; the cluster knows.
	ReclaimPolicy string `json:"reclaimPolicy"`
}

// Claim is one PersistentVolumeClaim's actual state.
type Claim struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	// Phase is Bound, Pending, or Lost.
	Phase string `json:"phase"`

	// StorageClass is the class actually in effect, which may be the cluster default rather
	// than anything the manifest names.
	StorageClass string `json:"storageClass,omitempty"`

	// Capacity is what the volume is right now, as a Kubernetes quantity such as "100Gi". It is
	// the bound capacity, not the request — those differ, and only the bound one constrains a
	// shrink.
	Capacity string `json:"capacity,omitempty"`
}

// Workload is one Deployment, StatefulSet, or DaemonSet.
type Workload struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Replicas  int32  `json:"replicas"`
}

// Validate reports whether a decoded snapshot is coherent enough to use.
//
// A snapshot that fails this is refused rather than partially applied. Context that is wrong is
// worse than context that is missing: the engine is designed to work without any, and a
// half-read file would enrich some findings and silently not others.
func (s *Snapshot) Validate() error {
	if s == nil {
		return fmt.Errorf("snapshot is nil")
	}
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version %q is not supported; this build reads %q",
			s.SchemaVersion, SchemaVersion)
	}
	if !s.Kind.Valid() {
		return fmt.Errorf("kind %q is not a snapshot kind; want %q or %q",
			s.Kind, KindPostgres, KindKubernetes)
	}
	if s.SourceFingerprint == "" {
		return fmt.Errorf("sourceFingerprint is empty, so this snapshot cannot be attributed to any source")
	}
	if s.CollectedAt.IsZero() {
		return fmt.Errorf("collectedAt is unset, so the snapshot's age cannot be judged")
	}

	switch s.Kind {
	case KindPostgres:
		if s.Postgres == nil {
			return fmt.Errorf("kind is %q but no postgres data is present", KindPostgres)
		}
	case KindKubernetes:
		if s.Kubernetes == nil {
			return fmt.Errorf("kind is %q but no kubernetes data is present", KindKubernetes)
		}
	}

	return nil
}

// Age reports how old the snapshot is as of now.
func (s *Snapshot) Age(now time.Time) time.Duration {
	return now.Sub(s.CollectedAt)
}

// Canonicalize sorts every collection into a total order.
//
// It is called after collection and after decoding. A snapshot is an input to a certificate, and
// certificates must be byte-identical for identical input — so a file whose rows arrived in
// whatever order the server returned them would make the digest depend on the database's mood.
func (s *Snapshot) Canonicalize() {
	if s.Postgres != nil {
		sort.Slice(s.Postgres.Tables, func(i, j int) bool {
			return less2(s.Postgres.Tables[i].Schema, s.Postgres.Tables[i].Name,
				s.Postgres.Tables[j].Schema, s.Postgres.Tables[j].Name)
		})
		sort.Slice(s.Postgres.Indexes, func(i, j int) bool {
			return less3(s.Postgres.Indexes[i].Schema, s.Postgres.Indexes[i].Table, s.Postgres.Indexes[i].Name,
				s.Postgres.Indexes[j].Schema, s.Postgres.Indexes[j].Table, s.Postgres.Indexes[j].Name)
		})
		sort.Slice(s.Postgres.Columns, func(i, j int) bool {
			return less3(s.Postgres.Columns[i].Schema, s.Postgres.Columns[i].Table, s.Postgres.Columns[i].Name,
				s.Postgres.Columns[j].Schema, s.Postgres.Columns[j].Table, s.Postgres.Columns[j].Name)
		})
	}

	if s.Kubernetes != nil {
		sort.Slice(s.Kubernetes.StorageClasses, func(i, j int) bool {
			return s.Kubernetes.StorageClasses[i].Name < s.Kubernetes.StorageClasses[j].Name
		})
		sort.Slice(s.Kubernetes.Claims, func(i, j int) bool {
			return less2(s.Kubernetes.Claims[i].Namespace, s.Kubernetes.Claims[i].Name,
				s.Kubernetes.Claims[j].Namespace, s.Kubernetes.Claims[j].Name)
		})
		sort.Slice(s.Kubernetes.Workloads, func(i, j int) bool {
			return less3(s.Kubernetes.Workloads[i].Namespace, s.Kubernetes.Workloads[i].Kind, s.Kubernetes.Workloads[i].Name,
				s.Kubernetes.Workloads[j].Namespace, s.Kubernetes.Workloads[j].Kind, s.Kubernetes.Workloads[j].Name)
		})
	}
}

func less2(a1, a2, b1, b2 string) bool {
	if a1 != b1 {
		return a1 < b1
	}
	return a2 < b2
}

func less3(a1, a2, a3, b1, b2, b3 string) bool {
	if a1 != b1 {
		return a1 < b1
	}
	if a2 != b2 {
		return a2 < b2
	}
	return a3 < b3
}

// qualify joins a schema and a name the way a person writes them, omitting the schema when it is
// the default one nobody types.
func qualify(schema, name string) string {
	if schema == "" || schema == "public" {
		return name
	}
	return schema + "." + name
}

// splitQualified separates "schema.table" into its parts. A bare name is assumed to be in the
// search path's default schema, which is what a migration that writes a bare name means.
func splitQualified(s string) (schema, name string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}
