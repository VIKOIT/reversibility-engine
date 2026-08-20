// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package kubernetes

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Name is the stable identifier for this analyzer.
const Name = "kubernetes"

// Analyzer classifies structural manifest differences against rules K8S001-K8S014.
//
// It holds no state: everything it needs lives for the duration of a single Analyze call, so
// one Analyzer is safe to share across goroutines.
type Analyzer struct{}

// New returns a Kubernetes analyzer.
func New() *Analyzer { return &Analyzer{} }

// Name implements analyzer.Analyzer.
func (a *Analyzer) Name() string { return Name }

// Supports claims .yaml and .yml files.
func (a *Analyzer) Supports(path string) bool {
	ext := strings.ToLower(extension(path))
	return ext == ".yaml" || ext == ".yml"
}

// Analyze diffs the manifests in a changeset object by object.
//
// Objects are matched across the two sides by apiVersion, kind, namespace, and name, per
// docs/RULES.md §2. Files whose content is byte-identical on both sides are still indexed but
// generate no findings of their own: they are context that rules such as K8S003 and K8S009
// need, not changes to grade.
func (a *Analyzer) Analyze(ctx context.Context, files []domain.ChangedFile) ([]domain.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s analyzer: %w", Name, err)
	}

	oldIndex, newIndex := index{}, index{}
	changed := map[objectKey]bool{}

	// Sorted for the same reason the Postgres analyzer sorts: two files defining the same
	// object would otherwise resolve last-one-wins by arrival order, making the verdict depend
	// on how the caller happened to order the changeset.
	ordered := make([]domain.ChangedFile, len(files))
	copy(ordered, files)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })

	var findings []domain.Finding

	for _, f := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s analyzer: %w", Name, err)
		}
		if !a.Supports(f.Path) {
			continue
		}

		// A file whose content is unchanged is context. Its objects are indexed so that other
		// rules can consult them, but nothing about them is reported.
		isContext := f.Status == domain.StatusModified && bytes.Equal(f.Previous, f.Current)

		if len(f.Previous) > 0 {
			objects, err := parseManifest(f.Path, f.Previous)
			if err != nil {
				// The previous side failing to parse does not indict the change: it is the
				// new state that is being deployed. Index what can be indexed and move on.
				objects = nil
			}
			for i := range objects {
				oldIndex[objects[i].key()] = &objects[i]
			}
		}

		if len(f.Current) > 0 {
			objects, err := parseManifest(f.Path, f.Current)
			if err != nil {
				// Fail closed. A manifest that will not parse is reported as UNKNOWN, which
				// grades F; it is never skipped and never assumed harmless.
				findings = append(findings, domain.Finding{
					RuleID:        "K8S014",
					File:          f.Path,
					Line:          0,
					Statement:     f.Path,
					Reversibility: domain.ReversibilityUnknown,
					LockHazard:    domain.LockNone,
					Rationale:     fmt.Sprintf("This manifest could not be parsed, so nothing in it can be classified: %v.", err),
				})
				continue
			}
			for i := range objects {
				key := objects[i].key()
				newIndex[key] = &objects[i]
				if !isContext {
					changed[key] = true
				}
			}
		}

		// Every object that existed before and is gone now is part of the change, unless the
		// file itself was untouched.
		if f.IsRemoved() {
			objects, err := parseManifest(f.Path, f.Previous)
			if err != nil {
				findings = append(findings, domain.Finding{
					RuleID:        "K8S014",
					File:          f.Path,
					Line:          0,
					Statement:     f.Path,
					Reversibility: domain.ReversibilityUnknown,
					LockHazard:    domain.LockNone,
					Rationale:     fmt.Sprintf("This manifest is being deleted but could not be parsed, so what the deletion destroys is unknown: %v.", err),
				})
				continue
			}
			for i := range objects {
				changed[objects[i].key()] = true
			}
		}
	}

	// Objects present before but absent now, in files that were modified rather than deleted.
	for key := range oldIndex {
		if _, stillPresent := newIndex[key]; !stillPresent {
			changed[key] = true
		}
	}

	for _, key := range sortedObjectKeys(changed) {
		c := change{key: key, old: oldIndex[key], new: newIndex[key], inChangeset: true}
		if c.old == nil && c.new == nil {
			continue
		}
		findings = append(findings, classify(c, oldIndex, newIndex)...)
	}

	domain.SortFindings(findings)
	return findings, nil
}

// sortedObjectKeys orders keys so that findings are generated deterministically. Map iteration
// order would otherwise leak into the certificate, which must be byte-identical across runs.
func sortedObjectKeys(set map[objectKey]bool) []objectKey {
	out := make([]objectKey, 0, len(set))
	for k, ok := range set {
		if ok {
			out = append(out, k)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// sortObjects orders objects by their key, for the same reason.
func sortObjects(objects []*object) {
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].key().String() < objects[j].key().String()
	})
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinDescriptions(objects []*object) string {
	parts := make([]string, 0, len(objects))
	for _, o := range objects {
		parts = append(parts, o.describe())
	}
	return strings.Join(parts, ", ")
}

// deepEqual compares two decoded YAML values structurally.
func deepEqual(a, b any) bool { return reflect.DeepEqual(a, b) }

// extension returns the final dot-suffix of a slash-separated path, or "" if there is none.
func extension(path string) string {
	slash := strings.LastIndex(path, "/")
	dot := strings.LastIndex(path, ".")
	if dot < 0 || dot < slash {
		return ""
	}
	return path[dot:]
}
