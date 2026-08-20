// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package render_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/render"
	"github.com/VIKOIT/reversibility-engine/pkg/certificate"
)

func renderTo(t *testing.T, format string, cert domain.ReversibilityCertificate) string {
	t.Helper()

	renderer, err := render.For(format)
	if err != nil {
		t.Fatalf("render.For(%q): %v", format, err)
	}

	var buf bytes.Buffer
	if err := renderer.Render(&buf, cert); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

func TestForKnownFormats(t *testing.T) {
	t.Parallel()

	for _, format := range render.Formats() {
		renderer, err := render.For(format)
		if err != nil {
			t.Fatalf("render.For(%q): %v", format, err)
		}
		if renderer.Format() != format {
			t.Errorf("renderer for %q reports format %q", format, renderer.Format())
		}
	}
}

func TestForRejectsUnknownFormat(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"", "yaml", "JSON", "html", "junit"} {
		if _, err := render.For(format); err == nil {
			t.Errorf("render.For(%q) returned nil error", format)
		}
	}
}

// Help text and error messages must not shuffle between runs.
func TestFormatsAreSorted(t *testing.T) {
	t.Parallel()

	got := render.Formats()
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("formats are not sorted: %v", got)
		}
	}
}

// A finding carries text from the analyzed repository, which is attacker-controlled in any pull
// request from a fork. A pipe must not be able to forge a table row, and a backtick must not be
// able to close a code span and start arbitrary markup in someone's PR.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	t.Parallel()

	hostile := domain.Finding{
		RuleID:        "PG001",
		File:          "migrations/evil|.sql",
		Line:          1,
		Statement:     "DROP TABLE x; -- ` | <script>alert(1)</script> | `",
		Reversibility: domain.ReversibilityIrreversible,
		LockHazard:    domain.LockExclusive,
		Rationale:     "a rationale with a | pipe and a ` backtick and <b>markup</b>",
	}

	cert := domain.ReversibilityCertificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          domain.GradeF,
		AIGateStatus:   domain.GateFail,
		Applicable:     true,
		InputDigest:    "abc",
		Findings:       []domain.Finding{hostile},
		UndoPlan:       []domain.UndoStep{},
		Blockers:       []string{"a blocker with a | pipe"},
		DownMigrations: []domain.DownMigrationStatus{},
	}

	out := renderTo(t, render.FormatMarkdown, cert)

	// Attacker-supplied markup must never reach a position where it would be interpreted.
	//
	// Inside a code span CommonMark escapes the content, so raw angle brackets there are inert —
	// and they have to stay raw, because SQL is full of "<", ">", "<=" and "<>" which
	// entity-escaping would mangle into unreadable output. Outside a code span it must be
	// entities. The renderer's own <details> and <sub> tags are template markup, not content,
	// so this looks for the injected string specifically.
	for i, line := range strings.Split(out, "\n") {
		outside := stripCodeSpans(line)
		for _, injected := range []string{"<script>", "</script>", "<b>", "</b>"} {
			if strings.Contains(outside, injected) {
				t.Errorf("line %d carries unescaped %s outside a code span: %q", i+1, injected, line)
			}
		}
	}

	// Every table row must have the column count the header declares. An unescaped pipe would
	// silently add one and corrupt the table.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "| 🔴") {
			continue
		}
		if got := strings.Count(line, "|") - strings.Count(line, `\|`); got != 7 {
			t.Errorf("finding row has %d unescaped pipes, want 7:\n%s", got, line)
		}
	}

	// The code span must be closed by the renderer, not by the attacker's backtick.
	for _, line := range strings.Split(out, "\n") {
		if strings.Count(line, "`")%2 != 0 {
			t.Errorf("line has an unbalanced code span, so its content escapes: %q", line)
		}
	}
}

// stripCodeSpans removes backtick-delimited spans, leaving the text that markdown will actually
// interpret.
func stripCodeSpans(line string) string {
	var b strings.Builder

	inSpan := false
	for _, r := range line {
		if r == '`' {
			inSpan = !inSpan
			continue
		}
		if !inSpan {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SQL comparison operators must survive into the rendered statement. Entity-escaping inside a
// code span would display "&lt;=" literally, which is worse than useless to a reviewer.
func TestMarkdownPreservesSQLOperators(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion,
		Grade:         domain.GradeB,
		AIGateStatus:  domain.GateFail,
		Applicable:    true,
		Findings: []domain.Finding{{
			RuleID:        "PG021",
			File:          "a.sql",
			Line:          1,
			Statement:     "ALTER TABLE t ADD CONSTRAINT c CHECK (total >= 0 AND a <> b)",
			Reversibility: domain.ReversibilityCostly,
			LockHazard:    domain.LockFullScan,
			Rationale:     "a rationale long enough to be a sentence",
			UndoStep:      "ALTER TABLE t DROP CONSTRAINT c;",
		}},
		UndoPlan:       []domain.UndoStep{},
		Blockers:       []string{},
		DownMigrations: []domain.DownMigrationStatus{},
	}

	out := renderTo(t, render.FormatMarkdown, cert)

	for _, want := range []string{"total >= 0", "a <> b"} {
		if !strings.Contains(out, want) {
			t.Errorf("statement lost %q; output was:\n%s", want, out)
		}
	}
	if strings.Contains(out, "&lt;") || strings.Contains(out, "&gt;") {
		t.Errorf("SQL operators were entity-escaped inside a code span:\n%s", out)
	}
}

// A statement containing a newline must not break out of its row.
func TestMarkdownFlattensNewlines(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion,
		Grade:         domain.GradeB,
		AIGateStatus:  domain.GateFail,
		Applicable:    true,
		Findings: []domain.Finding{{
			RuleID:        "PG012",
			File:          "a.sql",
			Line:          1,
			Statement:     "ALTER TABLE a\nRENAME TO b",
			Reversibility: domain.ReversibilityCostly,
			LockHazard:    domain.LockShort,
			Rationale:     "line one\nline two",
			UndoStep:      "ALTER TABLE b RENAME TO a;",
		}},
		UndoPlan:       []domain.UndoStep{},
		Blockers:       []string{},
		DownMigrations: []domain.DownMigrationStatus{},
	}

	for _, line := range strings.Split(renderTo(t, render.FormatMarkdown, cert), "\n") {
		if strings.HasPrefix(line, "|") && strings.Count(line, "|") < 2 {
			t.Errorf("a table row was split across lines: %q", line)
		}
	}
}

// The markdown must answer "can I merge this" without scrolling.
func TestMarkdownLeadsWithTheVerdict(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion, Grade: domain.GradeF, AIGateStatus: domain.GateFail,
		Applicable: true, Findings: []domain.Finding{}, UndoPlan: []domain.UndoStep{},
		Blockers: []string{}, DownMigrations: []domain.DownMigrationStatus{},
	}

	out := renderTo(t, render.FormatMarkdown, cert)
	first := strings.SplitN(out, "\n", 2)[0]

	if !strings.Contains(first, "Grade F") {
		t.Errorf("first line does not carry the grade: %q", first)
	}
}

// A changeset the engine has no opinion on must say so. A bare A would read as an endorsement.
func TestMarkdownStatesWhenNotApplicable(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion, Grade: domain.GradeA, AIGateStatus: domain.GatePass,
		Applicable: false, Findings: []domain.Finding{}, UndoPlan: []domain.UndoStep{},
		Blockers: []string{}, DownMigrations: []domain.DownMigrationStatus{},
	}

	out := renderTo(t, render.FormatMarkdown, cert)
	if !strings.Contains(out, "no opinion") {
		t.Errorf("an inapplicable certificate does not say so:\n%s", out)
	}
}

// The JSON renderer emits the public schema, which is the contract external consumers pin.
func TestJSONEmitsThePublicSchema(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion, Grade: domain.GradeA, AIGateStatus: domain.GatePass,
		Applicable: true, InputDigest: "deadbeef",
		Findings: []domain.Finding{}, UndoPlan: []domain.UndoStep{},
		Blockers: []string{}, DownMigrations: []domain.DownMigrationStatus{},
	}

	var decoded certificate.Certificate
	if err := json.Unmarshal([]byte(renderTo(t, render.FormatJSON, cert)), &decoded); err != nil {
		t.Fatalf("output does not decode into the public schema: %v", err)
	}

	if decoded.Grade != certificate.GradeA || !decoded.Passed() {
		t.Errorf("decoded grade %q passed=%v, want A/true", decoded.Grade, decoded.Passed())
	}
	if decoded.SchemaVersion != certificate.SchemaVersion {
		t.Errorf("SchemaVersion = %q", decoded.SchemaVersion)
	}
}

// Empty slices must serialize as [] rather than null, or a consumer ranging over them breaks on
// a certificate that simply had nothing to report.
func TestJSONEmitsEmptyArraysNotNull(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion, Grade: domain.GradeA, AIGateStatus: domain.GatePass,
		Findings: []domain.Finding{}, UndoPlan: []domain.UndoStep{},
		Blockers: []string{}, DownMigrations: []domain.DownMigrationStatus{},
	}

	out := renderTo(t, render.FormatJSON, cert)
	if strings.Contains(out, "null") {
		t.Errorf("JSON output contains null:\n%s", out)
	}
	for _, field := range []string{`"findings": []`, `"undoPlan": []`, `"blockers": []`, `"downMigrations": []`} {
		if !strings.Contains(out, field) {
			t.Errorf("expected %s in output:\n%s", field, out)
		}
	}
}

// UNKNOWN must be an error, not a warning. Downgrading it would let a change nobody understood
// pass a code-scanning gate — the fail-open this product exists to prevent.
func TestSARIFSeverityMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reversibility domain.Reversibility
		want          string
	}{
		{domain.ReversibilityIrreversible, "error"},
		{domain.ReversibilityUnknown, "error"},
		{domain.ReversibilityCostly, "warning"},
		{domain.ReversibilityReversible, "note"},
	}

	for _, tt := range tests {
		t.Run(string(tt.reversibility), func(t *testing.T) {
			t.Parallel()

			cert := domain.ReversibilityCertificate{
				SchemaVersion: domain.SchemaVersion, Grade: domain.GradeF, AIGateStatus: domain.GateFail,
				Applicable: true,
				Findings: []domain.Finding{{
					RuleID: "TEST", File: "a.sql", Line: 3, Statement: "SELECT 1",
					Reversibility: tt.reversibility, LockHazard: domain.LockNone,
					Rationale: "a rationale long enough to be a sentence",
				}},
				UndoPlan: []domain.UndoStep{}, Blockers: []string{}, DownMigrations: []domain.DownMigrationStatus{},
			}

			var log struct {
				Runs []struct {
					Tool struct {
						Driver struct {
							Rules []struct {
								ID                   string `json:"id"`
								DefaultConfiguration struct {
									Level string `json:"level"`
								} `json:"defaultConfiguration"`
							} `json:"rules"`
						} `json:"driver"`
					} `json:"tool"`
					Results []struct {
						Level     string `json:"level"`
						Locations []struct {
							PhysicalLocation struct {
								ArtifactLocation struct {
									URI string `json:"uri"`
								} `json:"artifactLocation"`
								Region *struct {
									StartLine int `json:"startLine"`
								} `json:"region"`
							} `json:"physicalLocation"`
						} `json:"locations"`
					} `json:"results"`
				} `json:"runs"`
			}

			if err := json.Unmarshal([]byte(renderTo(t, render.FormatSARIF, cert)), &log); err != nil {
				t.Fatalf("decoding SARIF: %v", err)
			}

			if len(log.Runs) != 1 || len(log.Runs[0].Results) != 1 {
				t.Fatalf("expected one run with one result")
			}

			result := log.Runs[0].Results[0]
			if result.Level != tt.want {
				t.Errorf("result level = %q, want %q", result.Level, tt.want)
			}
			if len(log.Runs[0].Tool.Driver.Rules) != 1 {
				t.Fatalf("expected one rule declaration")
			}
			if got := log.Runs[0].Tool.Driver.Rules[0].DefaultConfiguration.Level; got != tt.want {
				t.Errorf("rule level = %q, want %q", got, tt.want)
			}

			if len(result.Locations) != 1 {
				t.Fatalf("expected one location")
			}
			if got := result.Locations[0].PhysicalLocation.ArtifactLocation.URI; got != "a.sql" {
				t.Errorf("location URI = %q", got)
			}
			if result.Locations[0].PhysicalLocation.Region == nil ||
				result.Locations[0].PhysicalLocation.Region.StartLine != 3 {
				t.Errorf("region does not carry the line number")
			}
		})
	}
}

// A finding with no file — an engine panic — must not fabricate a location that points nowhere.
func TestSARIFOmitsLocationWhenThereIsNoFile(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion, Grade: domain.GradeF, AIGateStatus: domain.GateFail,
		Applicable: true,
		Findings: []domain.Finding{{
			RuleID: domain.RuleEnginePanic, Reversibility: domain.ReversibilityUnknown,
			LockHazard: domain.LockExclusive, Rationale: "the engine panicked",
		}},
		UndoPlan: []domain.UndoStep{}, Blockers: []string{"panic"}, DownMigrations: []domain.DownMigrationStatus{},
	}

	out := renderTo(t, render.FormatSARIF, cert)

	var decoded any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if strings.Contains(out, `"uri": ""`) {
		t.Errorf("SARIF emitted an empty artifact URI:\n%s", out)
	}
}

// SARIF requires each rule to be declared once. Findings repeat rule IDs constantly.
func TestSARIFDeduplicatesAndSortsRules(t *testing.T) {
	t.Parallel()

	makeFinding := func(rule string) domain.Finding {
		return domain.Finding{
			RuleID: rule, File: "a.sql", Line: 1, Statement: "x",
			Reversibility: domain.ReversibilityCostly, LockHazard: domain.LockNone,
			Rationale: "a rationale long enough to be a sentence",
		}
	}

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion, Grade: domain.GradeC, AIGateStatus: domain.GateFail,
		Applicable: true,
		Findings: []domain.Finding{
			makeFinding("PG014"), makeFinding("PG001"), makeFinding("PG014"), makeFinding("PG007"),
		},
		UndoPlan: []domain.UndoStep{}, Blockers: []string{}, DownMigrations: []domain.DownMigrationStatus{},
	}

	var log struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Rules []struct {
						ID string `json:"id"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
		} `json:"runs"`
	}

	if err := json.Unmarshal([]byte(renderTo(t, render.FormatSARIF, cert)), &log); err != nil {
		t.Fatalf("decoding SARIF: %v", err)
	}

	rules := log.Runs[0].Tool.Driver.Rules
	if len(rules) != 3 {
		t.Fatalf("got %d rule declarations, want 3 (deduplicated)", len(rules))
	}
	for i, want := range []string{"PG001", "PG007", "PG014"} {
		if rules[i].ID != want {
			t.Errorf("rule %d = %q, want %q", i, rules[i].ID, want)
		}
	}
}

// A correctly detected destructive migration is the tool working, not the tool failing. Marking
// the invocation unsuccessful would make code scanning treat it as a broken upload.
func TestSARIFReportsExecutionSuccessfulEvenOnGradeF(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion, Grade: domain.GradeF, AIGateStatus: domain.GateFail,
		Applicable: true, InputDigest: "abc",
		Findings: []domain.Finding{}, UndoPlan: []domain.UndoStep{},
		Blockers: []string{"irreversible"}, DownMigrations: []domain.DownMigrationStatus{},
	}

	var log struct {
		Runs []struct {
			Invocations []struct {
				ExecutionSuccessful bool `json:"executionSuccessful"`
				Properties          struct {
					Grade        string `json:"grade"`
					AIGateStatus string `json:"aiGateStatus"`
				} `json:"properties"`
			} `json:"invocations"`
		} `json:"runs"`
	}

	if err := json.Unmarshal([]byte(renderTo(t, render.FormatSARIF, cert)), &log); err != nil {
		t.Fatalf("decoding SARIF: %v", err)
	}

	inv := log.Runs[0].Invocations[0]
	if !inv.ExecutionSuccessful {
		t.Error("executionSuccessful = false for a grade F run")
	}
	if inv.Properties.Grade != "F" || inv.Properties.AIGateStatus != "FAIL" {
		t.Errorf("invocation properties = %+v, want grade F / FAIL", inv.Properties)
	}
}
