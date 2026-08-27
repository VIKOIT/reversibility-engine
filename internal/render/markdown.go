// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/terraform"
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
	// F now covers three different failures, and the summary names all three: a reader whose
	// migration will not even apply should not be told it would lose data on rollback.
	domain.GradeF: "**Not reversible.** Rolling this back would lose data, the engine could not determine what the change does, or the change will not apply at all.",

	// N/A is not a verdict about the change; it is the absence of one. The summary has to say
	// that in the first line, because the first line is all most readers see, and the line that
	// used to appear here said "Fully reversible" about a changeset nobody had read.
	domain.GradeNotApplicable: "**Not assessed.** The engine did not analyze this change, so it is making no claim about whether it can be rolled back.",
}

// The disclaiming sentences, named so that TestProseAndFieldsNeverDisagree can assert on them
// directly rather than matching on wording that is free to change.
//
// They exist because the prose and the fields disagreed once and it shipped: the markdown said
// "the engine has no opinion on it" three lines under a green **PASS**. A reader resolves that
// contradiction in favour of the badge every time.
const (
	// DisclaimerNoCandidates is printed when there was genuinely nothing to assess.
	DisclaimerNoCandidates = "_No PostgreSQL migrations, Kubernetes manifests, or Terraform plans were found in this change, " +
		"so there was nothing for the engine to assess. **This is not a pass** — it is the absence of a verdict._"

	// DisclaimerUnsupportedContent is printed when there was something to assess and no
	// analyzer could. Blockers names the files immediately below it.
	DisclaimerUnsupportedContent = "_This change contains files that may be migrations, and no analyzer in this engine can read them. " +
		"**Reversibility was not assessed.** Do not merge this on the strength of this certificate._"
)

var reversibilityIcon = map[domain.Reversibility]string{
	domain.ReversibilityReversible:   "🟢",
	domain.ReversibilityCostly:       "🟡",
	domain.ReversibilityIrreversible: "🔴",
	domain.ReversibilityUnknown:      "⚫",
	domain.ReversibilityWillFail:     "🛑",
}

// Render implements Renderer.
func (Markdown) Render(w io.Writer, cert domain.ReversibilityCertificate) error {
	var b strings.Builder

	writeHeader(&b, cert)
	writeBlockers(&b, cert)

	// Above the findings, deliberately. A reader must never have to infer what was skipped, and
	// a list of what the engine *did* find, printed first, is exactly what makes an incomplete
	// analysis look complete.
	writeUnanalyzed(&b, cert)

	writeFindings(&b, cert)
	writeWaived(&b, cert)
	writeUndoPlan(&b, cert)
	writeDownMigrations(&b, cert)
	writeContextWarnings(&b, cert)
	writeUnclassifiedTypes(&b, cert)
	writeFooter(&b, cert)

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("render markdown: %w", err)
	}
	return nil
}

func writeHeader(b *strings.Builder, cert domain.ReversibilityCertificate) {
	var gate string
	switch cert.AIGateStatus {
	case domain.GatePass:
		gate = "✅ PASS"
	case domain.GateNotApplicable:
		// Not a green check and not a red cross. A grey dash is the honest rendering of a gate
		// that was never evaluated, and it is visually distinct from both at a glance.
		gate = "➖ NOT APPLICABLE"
	default:
		gate = "❌ FAIL"
	}

	fmt.Fprintf(b, "## Reversibility Certificate — Grade %s\n\n", cert.Grade)

	if summary, ok := gradeSummary[cert.Grade]; ok {
		fmt.Fprintf(b, "%s\n\n", summary)
	}

	// Why there is no verdict, in the reader's terms. The two cases need different words: one
	// is "nothing here concerns me", the other is "something here concerns me and I cannot read
	// it", and telling a reviewer the second when it is the first — or the reverse — is how a
	// disclaimer stops being read at all.
	switch cert.Outcome {
	case domain.OutcomeNoCandidates:
		b.WriteString(DisclaimerNoCandidates + "\n\n")
	case domain.OutcomeUnsupportedContent:
		b.WriteString(DisclaimerUnsupportedContent + "\n\n")
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

	// Coverage is shown only when it is PARTIAL. A FULL row on every certificate is a row
	// readers learn to skip, and this is the row that matters on the one change where it is not
	// full. Empty coverage — a certificate from before 1.6.0, or one assembled wrongly — is not
	// FULL and so is shown.
	if !cert.Coverage.Full() {
		fmt.Fprintf(b, "| **Coverage** | ⚠️ PARTIAL — %s not analyzed |\n", plural(len(cert.UnanalyzedFiles), "file"))
	}

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

	// The heading and the sentence under it differ by outcome. Under UNSUPPORTED_CONTENT these
	// lines are not accusations against the change — they are a list of what the engine could
	// not read, and calling them blockers would suggest the migrations were examined and found
	// wanting.
	if cert.Outcome == domain.OutcomeUnsupportedContent {
		b.WriteString("### Not assessed\n\n")
		b.WriteString("The engine cannot read the following, so it has measured nothing. " +
			"An autonomous agent must not merge this change.\n\n")
	} else {
		b.WriteString("### Blockers\n\n")
		b.WriteString("This change cannot be merged by an autonomous agent. Each item below is a reason on its own.\n\n")
	}
	for _, blocker := range cert.Blockers {
		fmt.Fprintf(b, "- %s\n", blocker)
	}
	b.WriteString("\n")
}

// writeUnanalyzed names every file the engine did not read.
//
// Every one of them, never a count and never a sample. The whole purpose of the coverage axis is
// that a reviewer can see the specific files and decide, and a truncated list would put them
// back where they started — knowing something was skipped and not what.
//
// The wording is careful not to accuse the files of anything. They are unread because of a limit
// in this engine, and a certificate that implied otherwise would be inventing severity from
// ignorance, which is the mirror of the bug that made this field necessary.
func writeUnanalyzed(b *strings.Builder, cert domain.ReversibilityCertificate) {
	if len(cert.UnanalyzedFiles) == 0 {
		return
	}

	b.WriteString("### Not analyzed\n\n")
	b.WriteString("This engine could not read the following files. **They are not part of the grade above** — " +
		"neither for it nor against it. The grade describes what was read; this list is what was not.\n\n")
	b.WriteString("| File | Why |\n| --- | --- |\n")

	for _, u := range cert.UnanalyzedFiles {
		fmt.Fprintf(b, "| `%s` | %s |\n", mdCode(u.Path), mdEscape(u.Reason))
	}

	b.WriteString("\nAn autonomous agent will not merge this change: the AI merge gate requires full coverage. " +
		"A human reviewer can see exactly what was skipped and decide.\n\n")
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
		band := ""
		if f.Context.LockDurationBand != "" {
			// The band is what scoring uses, so it is shown beside the number it came from
			// rather than left for a reader to infer from a duration and a table of thresholds.
			band = fmt.Sprintf(" — %s", f.Context.LockDurationBand)
		}
		fmt.Fprintf(b, "  - _Estimated %s lock: %s%s. An approximation from table size, not a measurement._\n",
			f.LockHazard, mdEscape(f.Context.EstimatedLockDuration), band)
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

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
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

// writeUnclassifiedTypes is the growth loop: when a plan destroys a resource type the catalog
// does not know, the certificate hands back everything needed to contribute it.
//
// ONE snippet and ONE link covering every unknown type in the plan. Six unknown types meaning
// six paste operations is where somebody gives up and disables the gate instead, which costs
// more safety than any one classification buys back.
//
// Nothing is sent anywhere. This is a link in output the reader is already looking at.
func writeUnclassifiedTypes(b *strings.Builder, cert domain.ReversibilityCertificate) {
	types := terraform.UnclassifiedTypes(allCertificateFindings(cert))
	if len(types) == 0 {
		return
	}

	b.WriteString("### Unclassified Terraform types\n\n")
	fmt.Fprintf(b, "This plan destroys %d resource type(s) the catalog does not classify, so the engine cannot say what destroying them costs. Classify them locally, and please contribute them upstream so nobody else hits the same gap.\n\n",
		len(types))

	b.WriteString("Add to `.reversibility.yml`:\n\n```yaml\n")
	b.WriteString(terraform.PolicySnippet(types))
	b.WriteString("```\n\n")

	fmt.Fprintf(b, "[Contribute these to the catalog](%s)\n\n", terraform.IssueURL(types, cert.CatalogVersion))
}

// allCertificateFindings returns findings and waived findings together, so a waived TF010 still
// contributes its type to the suggestion. The gap in the catalog is the same gap either way.
func allCertificateFindings(cert domain.ReversibilityCertificate) []domain.Finding {
	out := make([]domain.Finding, 0, len(cert.Findings)+len(cert.Waived))
	out = append(out, cert.Findings...)
	for _, w := range cert.Waived {
		out = append(out, w.Finding)
	}
	return out
}
