// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package terraform

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/VIKOIT/reversibility-engine/catalog"
)

// Class is what destroying a resource type costs.
type Class string

// The complete set of classes.
const (
	// ClassStateful means destroying it destroys data, destroys an identity that re-applying
	// the same configuration cannot recreate, or destroys a recovery capability a future
	// rollback would depend on.
	ClassStateful Class = "STATEFUL"

	// ClassStateless means destroying it is disruptive and fully recreatable.
	ClassStateless Class = "STATELESS"
)

// Valid reports whether c is a defined class. The zero value is not one: a type nobody
// classified must not read as stateless.
func (c Class) Valid() bool { return c == ClassStateful || c == ClassStateless }

// AtLeastAsSevereAs reports whether c is at least as severe as other. STATEFUL outranks
// STATELESS, and that ordering is what makes "tighten only" checkable.
func (c Class) AtLeastAsSevereAs(other Class) bool {
	return c.severity() >= other.severity()
}

func (c Class) severity() int {
	if c == ClassStateful {
		return 1
	}
	return 0
}

// Entry is one classified resource type.
type Entry struct {
	Type  string `json:"type"`
	Class Class  `json:"class"`

	// Evidence is the provider documentation supporting the classification. It is REQUIRED:
	// a classification nobody can check is an opinion, and this table has to be reviewable by
	// somebody who did not write it.
	Evidence string `json:"evidence"`

	// AddedIn is the catalog version that introduced the entry.
	AddedIn string `json:"added_in"`
}

// Catalog is one provider's classified resource types.
type Catalog struct {
	// Version identifies the catalog's content. It travels onto the certificate, because the
	// same plan can grade differently under two catalogs and a reader needs to know which one
	// produced the verdict.
	Version string `json:"catalog_version"`

	Provider string  `json:"provider"`
	Entries  []Entry `json:"entries"`

	// digest is the SHA-256 over the resolved entries, mixed into the certificate's input
	// digest whenever a plan was actually analyzed.
	digest string

	byType map[string]Entry
}

// LoadEmbedded reads the catalog compiled into this binary.
//
// There is no network path here and there is none anywhere in analysis. `revctl catalog update`
// is a separate, explicit command; `revctl check` reads this and nothing else, so the tool works
// identically on an air-gapped runner for the lifetime of the binary.
func LoadEmbedded(provider catalog.TerraformProvider) (*Catalog, error) {
	raw, err := catalog.Terraform(provider)
	if err != nil {
		return nil, fmt.Errorf("reading the embedded catalog: %w", err)
	}
	return ParseCatalog(raw)
}

// ParseCatalog decodes and validates a catalog document.
func ParseCatalog(raw []byte) (*Catalog, error) {
	var c Catalog

	// Strict: an unknown key means the file was written against a schema this build does not
	// know, and reading the half of it that happens to parse would classify resources from a
	// document nobody verified.
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return nil, fmt.Errorf("decoding the catalog: %w", err)
	}

	if strings.TrimSpace(c.Version) == "" {
		return nil, fmt.Errorf("the catalog has no catalog_version, so a verdict could not be attributed to it")
	}
	if strings.TrimSpace(c.Provider) == "" {
		return nil, fmt.Errorf("the catalog names no provider")
	}

	c.byType = make(map[string]Entry, len(c.Entries))
	for i, e := range c.Entries {
		if err := validateEntry(e); err != nil {
			return nil, fmt.Errorf("entries[%d] (%s): %w", i, e.Type, err)
		}
		if _, dup := c.byType[e.Type]; dup {
			return nil, fmt.Errorf("entries[%d]: %s is classified twice", i, e.Type)
		}
		c.byType[e.Type] = e
	}

	c.digest = digestEntries(c)
	return &c, nil
}

// validateEntry enforces the two fields no entry may omit.
func validateEntry(e Entry) error {
	if strings.TrimSpace(e.Type) == "" {
		return fmt.Errorf("type is required")
	}
	if !e.Class.Valid() {
		return fmt.Errorf("class %q is not %s or %s", e.Class, ClassStateful, ClassStateless)
	}
	if strings.TrimSpace(e.Evidence) == "" {
		return fmt.Errorf("evidence is required: a classification nobody can check is an opinion")
	}
	if !strings.HasPrefix(e.Evidence, "http://") && !strings.HasPrefix(e.Evidence, "https://") {
		return fmt.Errorf("evidence %q is not a link", e.Evidence)
	}
	return nil
}

// Lookup returns the classification for a resource type.
func (c *Catalog) Lookup(resourceType string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	e, ok := c.byType[resourceType]
	return e, ok
}

// Digest is the SHA-256 over the catalog's classifications.
func (c *Catalog) Digest() string {
	if c == nil {
		return ""
	}
	return c.digest
}

// Coverage reports how many types are classified, for the honest number in the docs.
func (c *Catalog) Coverage() (stateful, stateless int) {
	if c == nil {
		return 0, 0
	}
	for _, e := range c.Entries {
		if e.Class == ClassStateful {
			stateful++
			continue
		}
		stateless++
	}
	return stateful, stateless
}

// digestEntries hashes the classifications, sorted, so the digest depends on what the catalog
// says rather than on the order somebody wrote it in. Comments and formatting do not move it;
// a changed class does.
func digestEntries(c Catalog) string {
	types := make([]string, 0, len(c.Entries))
	for _, e := range c.Entries {
		types = append(types, e.Type)
	}
	sort.Strings(types)

	h := sha256.New()
	writeField(h, []byte(c.Version))
	writeField(h, []byte(c.Provider))
	for _, t := range types {
		writeField(h, []byte(t))
		writeField(h, []byte(c.byType[t].Class))
	}

	return hex.EncodeToString(h.Sum(nil))
}

func writeField(h interface{ Write([]byte) (int, error) }, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))

	_, _ = h.Write(length[:])
	_, _ = h.Write(b)
}
