package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// SARIF renders the certificate as SARIF 2.1.0, the format GitHub code scanning ingests.
//
// This is what puts findings inline on the diff, next to the migration that caused them, rather
// than in a comment a reviewer has to correlate by hand.
type SARIF struct{}

// Format implements Renderer.
func (SARIF) Format() string { return FormatSARIF }

// SARIF identity constants.
//
// toolVersion is deliberately fixed rather than derived from a build stamp: a version that moved
// with every build would make two runs over identical input produce different bytes, and
// determinism is a hard requirement. It tracks the certificate schema, which is what actually
// changes the meaning of the output.
const (
	sarifVersion = "2.1.0"
	sarifSchema  = "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json"

	toolName    = "reversibility-engine"
	toolURI     = "https://github.com/VIKOIT/reversibility-engine"
	toolVersion = domain.SchemaVersion
)

// SARIF severity levels.
const (
	levelError   = "error"
	levelWarning = "warning"
	levelNote    = "note"
)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool        sarifTool         `json:"tool"`
	Results     []sarifResult     `json:"results"`
	Invocations []sarifInvocation `json:"invocations"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	ShortDescription     sarifText         `json:"shortDescription"`
	FullDescription      sarifText         `json:"fullDescription"`
	DefaultConfiguration sarifRuleDefaults `json:"defaultConfiguration"`
	Properties           sarifRuleProps    `json:"properties"`
}

type sarifRuleDefaults struct {
	Level string `json:"level"`
}

type sarifRuleProps struct {
	Reversibility string   `json:"reversibility"`
	Tags          []string `json:"tags"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifText       `json:"message"`
	Locations []sarifLocation `json:"locations"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           *sarifRegion  `json:"region,omitempty"`
}

type sarifArtifact struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

// sarifInvocation carries the overall verdict, so a consumer reading only the SARIF still learns
// whether the gate passed rather than having to re-derive it from the results.
type sarifInvocation struct {
	ExecutionSuccessful bool          `json:"executionSuccessful"`
	ExitCodeDescription string        `json:"exitCodeDescription"`
	Properties          sarifRunProps `json:"properties"`
}

type sarifRunProps struct {
	Grade         string `json:"grade"`
	AIGateStatus  string `json:"aiGateStatus"`
	Applicable    bool   `json:"applicable"`
	InputDigest   string `json:"inputDigest"`
	SchemaVersion string `json:"certificateSchemaVersion"`
}

// Render implements Renderer.
func (SARIF) Render(w io.Writer, cert domain.ReversibilityCertificate) error {
	log := sarifLog{
		Schema:  sarifSchema,
		Version: sarifVersion,
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:           toolName,
				Version:        toolVersion,
				InformationURI: toolURI,
				Rules:          sarifRules(cert.Findings),
			}},
			Results: sarifResults(cert.Findings),
			Invocations: []sarifInvocation{{
				// A failing grade is a successful execution: the engine did its job. Reporting
				// otherwise would make code scanning treat a correctly-detected destructive
				// migration as a broken tool.
				ExecutionSuccessful: true,
				ExitCodeDescription: fmt.Sprintf("grade %s, AI merge gate %s", cert.Grade, cert.AIGateStatus),
				Properties: sarifRunProps{
					Grade:         string(cert.Grade),
					AIGateStatus:  string(cert.AIGateStatus),
					Applicable:    cert.Applicable,
					InputDigest:   cert.InputDigest,
					SchemaVersion: cert.SchemaVersion,
				},
			}},
		}},
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(log); err != nil {
		return fmt.Errorf("render sarif: %w", err)
	}
	return nil
}

// sarifRules describes each distinct rule that fired.
//
// SARIF requires the rule set to be declared once and referenced by results. Deduplicating by ID
// and sorting keeps the array stable, which map iteration alone would not.
func sarifRules(findings []domain.Finding) []sarifRule {
	seen := map[string]domain.Finding{}
	for _, f := range findings {
		if _, ok := seen[f.RuleID]; !ok {
			seen[f.RuleID] = f
		}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	rules := make([]sarifRule, 0, len(ids))
	for _, id := range ids {
		f := seen[id]
		rules = append(rules, sarifRule{
			ID:                   id,
			Name:                 id,
			ShortDescription:     sarifText{Text: shortDescription(f)},
			FullDescription:      sarifText{Text: f.Rationale},
			DefaultConfiguration: sarifRuleDefaults{Level: sarifLevel(f.Reversibility)},
			Properties: sarifRuleProps{
				Reversibility: string(f.Reversibility),
				Tags:          []string{"reversibility", "rollback", string(f.Reversibility)},
			},
		})
	}
	return rules
}

func sarifResults(findings []domain.Finding) []sarifResult {
	results := make([]sarifResult, 0, len(findings))

	for _, f := range findings {
		result := sarifResult{
			RuleID:  f.RuleID,
			Level:   sarifLevel(f.Reversibility),
			Message: sarifText{Text: sarifMessage(f)},
		}

		// A finding with no file is a property of the run itself, such as an engine panic.
		// SARIF requires a URI when a location is present, so it is better to omit the
		// location than to invent a path that points nowhere.
		if f.File != "" {
			location := sarifLocation{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifact{URI: f.File},
				},
			}
			if f.Line > 0 {
				location.PhysicalLocation.Region = &sarifRegion{StartLine: f.Line}
			}
			result.Locations = []sarifLocation{location}
		} else {
			result.Locations = []sarifLocation{}
		}

		results = append(results, result)
	}
	return results
}

// sarifLevel maps reversibility onto the severity code-scanning understands.
//
// Both IRREVERSIBLE and UNKNOWN are errors. Downgrading UNKNOWN to a warning would let a change
// nobody understood pass a scanning gate, which is the fail-open this product exists to prevent.
func sarifLevel(r domain.Reversibility) string {
	switch r {
	case domain.ReversibilityIrreversible, domain.ReversibilityUnknown:
		return levelError
	case domain.ReversibilityCostly:
		return levelWarning
	default:
		return levelNote
	}
}

func sarifMessage(f domain.Finding) string {
	if f.UndoStep == "" {
		return fmt.Sprintf("%s: %s No undo step exists for this change.", f.Reversibility, f.Rationale)
	}
	return fmt.Sprintf("%s: %s Undo: %s", f.Reversibility, f.Rationale, f.UndoStep)
}

func shortDescription(f domain.Finding) string {
	return fmt.Sprintf("%s change with %s lock hazard", f.Reversibility, f.LockHazard)
}
