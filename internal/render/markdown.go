// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Markdown renders the certificate for a human reading a pull request.
//
// The ordering is deliberate: verdict first, then why it is blocked, then the detail. Someone
// scanning this during an incident should get the answer in the first line and never have to
// scroll to find out whether they can merge.
type Markdown struct{}

// Format implements Renderer.
func (Markdown) Format() string { return FormatMarkdown }

// gradeSummary is the one-line verdict for each grade, written for a reader who has not read
// the documentation.
var gradeSummary = map[domain.Grade]string{
	domain.GradeA: "Fully reversible. This change can be rolled back with no data loss.",
	domain.GradeB: "Reversible at a cost. Rolling back is possible but slow, disruptive, or only safe within a window.",
	domain.GradeC: "Reversible with significant caveats. Review the findings before merging.",
	domain.GradeF: "**Not reversible.** Rolling this back would lose data, or the engine could not determine what it does.",
}

var reversibilityIcon = map[domain.Reversibility]string{
	domain.ReversibilityReversible:   "🟢",
	domain.ReversibilityCostly:       "🟡",
	domain.ReversibilityIrreversible: "🔴",
	domain.ReversibilityUnknown:      "⚫",
}

// Render implements Renderer.
func (Markdown) Render(w io.Writer, cert domain.ReversibilityCertificate) error {
	var b strings.Builder

	writeHeader(&b, cert)
	writeBlockers(&b, cert)
	writeFindings(&b, cert)
	writeWaived(&b, cert)
	writeUndoPlan(&b, cert)
	writeDownMigrations(&b, cert)
	writeContextWarnings(&b, cert)
	writeFooter(&b, cert)

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("render markdown: %w", err)
	}
	return nil
}

func writeHeader(b *strings.Builder, cert domain.ReversibilityCertificate) {
	gate := "❌ FAIL"
	if cert.AIGateStatus == domain.GatePass {
		gate = "✅ PASS"
	}

	fmt.Fprintf(b, "## Reversibility Certificate — Grade %s\n\n", cert.Grade)

	if summary, ok := gradeSummary[cert.Grade]; ok {
		fmt.Fprintf(b, "%s\n\n", summary)
	}

	// The changeset had nothing the engine understands. Saying so plainly matters: a silent A
	// would look like an endorsement it is not.
	if !cert.Applicable {
		b.WriteString("_No PostgreSQL migrations or Kubernetes manifests were found in this change, " +
			"so the engine has no opinion on it._\n\n")
	}

	fmt.Fprintf(b, "| | |\n| --- | --- |\n")
	fmt.Fprintf(b, "| **Grade** | %s |\n", cert.Grade)

	// The two grades are shown together only when they differ. Printing an identical pair on
	// every certificate would train readers to skip the row, which is the row that matters on
	// the one change where a waiver applied.
	if cert.EffectiveGrade != "" && cert.EffectiveGrade != cert.Grade {
		fmt.Fprintf(b, "| **Grade after waivers** | %s |\n", cert.EffectiveGrade)
	}

	fmt.Fprintf(b, "| **AI merge gate** | %s |\n", gate)
	fmt.Fprintf(b, "| **Findings** | %d |\n", len(cert.Findings))

	if len(cert.Waived) > 0 {
		fmt.Fprintf(b, "| **Waived** | %d |\n", len(cert.Waived))
	}

	b.WriteString("\n")
}

// writeWaived reports the findings a policy accepted.
//
// They are printed, never omitted. A waiver is a decision to accept a specific risk, and the
// only way a reviewer can judge whether that decision still holds is to see what was accepted,
// who accepted it, and when it lapses.
func writeWaived(b *strings.Builder, cert domain.ReversibilityCertificate) {
	if len(cert.Waived) == 0 {
		return
	}

	b.WriteString("### Waived\n\n")
	b.WriteString("A policy waiver accepted each of these. They still count toward the grade above — " +
		"a waiver accepts a risk, it does not remove one — and they no longer block the pipeline.\n\n")
	b.WriteString("| Rule | Location | Reversibility | Reason | Expires | Approved by |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")

	for _, w := range cert.Waived {
		approver := w.ApprovedBy
		if approver == "" {
			approver = "—"
		}

		fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s | %s |\n",
			mdCode(w.Finding.RuleID),
			mdEscape(findingLocation(w.Finding)),
			mdEscape(string(w.Finding.Reversibility)),
			mdEscape(w.Reason),
			mdEscape(w.Expires),
			mdEscape(approver))
	}

	b.WriteString("\n")
}

func writeBlockers(b *strings.Builder, cert domain.ReversibilityCertificate) {
	if len(cert.Blockers) == 0 {
		return
	}

	b.WriteString("### Blockers\n\n")
	b.WriteString("This change cannot be merged by an autonomous agent. Each item below is a reason on its own.\n\n")
	for _, blocker := range cert.Blockers {
		fmt.Fprintf(b, "- %s\n", blocker)
	}
	b.WriteString("\n")
}

func writeFindings(b *strings.Builder, cert domain.ReversibilityCertificate) {
	if len(cert.Findings) == 0 {
		return
	}

	b.WriteString("### Findings\n\n")
	b.WriteString("| | Rule | Location | Reversibility | Lock | Change |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")

	for _, f := range cert.Findings {
		fmt.Fprintf(b, "| %s | `%s` | %s | %s | %s | `%s` |\n",
			reversibilityIcon[f.Reversibility],
			f.RuleID,
			mdEscape(findingLocation(f)),
			f.Reversibility,
			f.LockHazard,
			mdCode(f.Statement),
		)
	}
	b.WriteString("\n")

	// The rationale is the part that changes someone's mind, so it gets room to breathe rather
	// than being squeezed into a table cell.
	b.WriteString("<details>\n<summary>Why each finding was classified this way</summary>\n\n")
	for _, f := range cert.Findings {
		fmt.Fprintf(b, "- **%s** at %s — %s\n", f.RuleID, mdEscape(findingLocation(f)), mdEscape(f.Rationale))
		writeFindingContext(b, f)
	}
	b.WriteString("\n</details>\n\n")
}

// writeFindingContext prints what a production snapshot added to one finding.
//
// It is nested under the rationale rather than given a column, because it is a sentence about
// this specific database and not a property of the rule. The estimate is labelled every time it
// appears: a number the reader learns to distrust is worse than no number.
func writeFindingContext(b *strings.Builder, f domain.Finding) {
	if f.Context == nil {
		return
	}

	if f.Context.ContextNote != "" {
		fmt.Fprintf(b, "  - _In production:_ %s\n", mdEscape(f.Context.ContextNote))
	}

	if f.Context.EstimatedLockDuration != "" {
		fmt.Fprintf(b, "  - _Estimated %s lock: %s — an approximation from table size, not a measurement._\n",
			f.LockHazard, mdEscape(f.Context.EstimatedLockDuration))
	}
}

// writeContextWarnings reports what was wrong with the snapshots supplied.
//
// Stale context is used and flagged rather than discarded: falling back to none would make the
// certificate quietly less informative at exactly the moment somebody stopped refreshing it.
func writeContextWarnings(b *strings.Builder, cert domain.ReversibilityCertificate) {
	if len(cert.ContextWarnings) == 0 {
		return
	}

	b.WriteString("### Production context\n\n")
	for _, w := range cert.ContextWarnings {
		fmt.Fprintf(b, "- ⚠️ %s\n", mdEscape(w))
	}
	b.WriteString("\n")
}

func writeUndoPlan(b *strings.Builder, cert domain.ReversibilityCertificate) {
	if len(cert.UndoPlan) == 0 {
		return
	}

	b.WriteString("### Undo plan\n\n")
	b.WriteString("Steps are in the order they must be run, unwinding the change from the last step applied.\n\n")
	b.WriteString("```sql\n")
	for _, step := range cert.UndoPlan {
		fmt.Fprintf(b, "%s\n", step)
	}
	b.WriteString("```\n\n")
}

func writeDownMigrations(b *strings.Builder, cert domain.ReversibilityCertificate) {
	if len(cert.DownMigrations) == 0 {
		return
	}

	b.WriteString("### Down migrations\n\n")
	b.WriteString("| Migration | Exists | Parses | Symmetric |\n")
	b.WriteString("| --- | --- | --- | --- |\n")

	for _, d := range cert.DownMigrations {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n",
			mdEscape(d.Migration), checkbox(d.Exists), checkbox(d.Parses), checkbox(d.Symmetric))
	}

	b.WriteString("\n_Symmetry is a heuristic and is advisory only; it never affects the grade on its own._\n\n")

	// Explaining a failed check where the reader is looking saves a trip to the source.
	var notes []string
	for _, d := range cert.DownMigrations {
		for _, note := range d.SymmetryNotes {
			notes = append(notes, fmt.Sprintf("- `%s`: %s", mdEscape(d.Migration), mdEscape(note)))
		}
	}
	if len(notes) > 0 {
		b.WriteString("<details>\n<summary>Down-migration notes</summary>\n\n")
		b.WriteString(strings.Join(notes, "\n"))
		b.WriteString("\n\n</details>\n\n")
	}
}

func writeFooter(b *strings.Builder, cert domain.ReversibilityCertificate) {
	fmt.Fprintf(b, "---\n\n<sub>Reversibility Engine · schema %s · input digest `%s`</sub>\n",
		cert.SchemaVersion, cert.InputDigest)
}

func checkbox(ok bool) string {
	if ok {
		return "✅"
	}
	return "❌"
}

func findingLocation(f domain.Finding) string {
	switch {
	case f.File == "":
		return "(whole changeset)"
	case f.Line > 0:
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	default:
		return f.File
	}
}

// mdEscape neutralizes the characters that would break out of a table cell.
//
// Findings carry text from the analyzed repository, and that text is attacker-controlled in any
// pull request from a fork. A pipe in a statement must not be able to forge a table row, and a
// backtick must not be able to close a code span and start arbitrary markup.
func mdEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"|", `\|`,
		"`", "'",
		"<", "&lt;",
		">", "&gt;",
		"\r", " ",
		"\n", " ",
	)
	return replacer.Replace(s)
}

// mdCode escapes text that is rendered inside a code span, where backticks and pipes are the
// only characters that can escape.
func mdCode(s string) string {
	replacer := strings.NewReplacer(
		"`", "'",
		"|", `\|`,
		"\r", " ",
		"\n", " ",
	)
	return replacer.Replace(s)
}
