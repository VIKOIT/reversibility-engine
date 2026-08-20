package fixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

// ExpectationFile is the name of the expectation document inside every fixture directory.
const ExpectationFile = "expected.json"

// Root locates testdata/fixtures by walking up from the working directory to the module root.
//
// Tests run with the working directory set to their own package, so a relative path would
// differ for every caller and break the moment a package moves. Anchoring on go.mod does not.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("fixture root: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", "fixtures"), nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("fixture root: no go.mod above the working directory: %w", os.ErrNotExist)
		}
		dir = parent
	}
}

// Finding is the asserted classification of one finding.
//
// It is a projection of domain.Finding, not the whole struct. Rationale and UndoStep wording
// are excluded on purpose: they are written for humans and will be reworded, and a test suite
// that breaks on copy edits trains people to stop reading failures.
type Finding struct {
	RuleID        string               `json:"ruleId"`
	File          string               `json:"file"`
	Line          int                  `json:"line"`
	Reversibility domain.Reversibility `json:"reversibility"`
	LockHazard    domain.LockHazard    `json:"lockHazard"`

	// WantUndoStep asserts only presence or absence. An IRREVERSIBLE finding must carry no
	// undo step, because offering one would be a lie.
	WantUndoStep bool `json:"wantUndoStep"`
}

// DownMigration is the asserted outcome of down-migration validation for one migration pair.
type DownMigration struct {
	Migration string `json:"migration"`
	Exists    bool   `json:"exists"`
	Parses    bool   `json:"parses"`
	Symmetric bool   `json:"symmetric"`
}

// Expectation is the contents of a fixture's expected.json.
type Expectation struct {
	// Rule is the rule ID this fixture exists to prove. A fixture whose Rule appears in no
	// finding is a fixture that proves nothing.
	Rule string `json:"rule"`

	// Note explains why the fixture is shaped the way it is, for whoever has to change it.
	Note string `json:"note"`

	Findings []Finding `json:"findings"`

	// DownMigrations is asserted only by the fixtures that exist to test down-migration
	// validation; elsewhere it is nil and not compared.
	DownMigrations []DownMigration `json:"downMigrations"`
}

// Case is one fixture: its directory name, the reference that resolves it, and what it asserts.
type Case struct {
	Name   string
	Ref    domain.ChangeRef
	Expect Expectation
}

// Load reads the expectation for a single fixture directory.
func Load(root, group, name string) (Expectation, error) {
	p := filepath.Join(root, group, name, ExpectationFile)

	raw, err := os.ReadFile(p)
	if err != nil {
		return Expectation{}, fmt.Errorf("fixture %s/%s: reading expectation: %w", group, name, err)
	}

	var exp Expectation
	// Unknown fields are rejected so that a typo in a fixture — "lockHazzard" — fails loudly
	// instead of silently asserting the zero value.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&exp); err != nil {
		return Expectation{}, fmt.Errorf("fixture %s/%s: parsing %s: %w", group, name, ExpectationFile, err)
	}

	if exp.Rule == "" {
		return Expectation{}, fmt.Errorf("fixture %s/%s: %s has no \"rule\" field: %w", group, name, ExpectationFile, domain.ErrInvalidChangedFile)
	}

	return exp, nil
}

// Cases enumerates every fixture in a group, sorted by name so that test output is stable.
func Cases(root, group string) ([]Case, error) {
	dir := filepath.Join(root, group)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("fixture group %q: %w", group, err)
	}

	var cases []Case
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		exp, err := Load(root, group, e.Name())
		if err != nil {
			return nil, err
		}

		cases = append(cases, Case{
			Name:   e.Name(),
			Ref:    provider.FixtureRef(group, e.Name()),
			Expect: exp,
		})
	}

	if len(cases) == 0 {
		return nil, fmt.Errorf("fixture group %q contains no fixtures: %w", group, domain.ErrProviderFailed)
	}

	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	return cases, nil
}

// Project reduces real findings to the fields an expectation asserts, so that a comparison
// failure shows only what the fixture actually claims.
func Project(findings []domain.Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		out = append(out, Finding{
			RuleID:        f.RuleID,
			File:          f.File,
			Line:          f.Line,
			Reversibility: f.Reversibility,
			LockHazard:    f.LockHazard,
			WantUndoStep:  f.UndoStep != "",
		})
	}
	return out
}
