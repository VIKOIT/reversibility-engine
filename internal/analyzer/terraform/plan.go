// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package terraform

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Supported plan format versions.
//
// An unrecognised version is UNKNOWN, never a best-effort guess: a format this build has not
// been written against may have moved a field this analyzer reads, and a classification derived
// from a misread field is exactly the confident wrong answer the product exists to prevent.
var supportedFormatVersions = map[string]bool{
	"1.0": true,
	"1.1": true,
}

// plan is the subset of `terraform show -json` this analyzer reads.
//
// Decoding is deliberately NOT strict about unknown fields. A plan carries a great deal this
// analyzer has no interest in — prior_state, configuration, output_changes, checks — and
// rejecting a plan for containing them would refuse every real file. The safety gate is the
// format version, which is checked exactly, not field-level pedantry.
//
// terraform.tfstate is never read, here or anywhere. It holds provider secrets in plaintext;
// the plan does not.
type plan struct {
	FormatVersion    string           `json:"format_version"`
	TerraformVersion string           `json:"terraform_version"`
	ResourceChanges  []resourceChange `json:"resource_changes"`
}

// resourceChange is one entry of the plan's resource_changes array.
type resourceChange struct {
	Address string `json:"address"`

	// Mode is "managed" or "data". Data sources are reads and are never classified.
	Mode string `json:"mode"`

	Type         string `json:"type"`
	Name         string `json:"name"`
	ProviderName string `json:"provider_name"`

	Change change `json:"change"`
}

// change is the actions and the two sides of one resource's transition.
type change struct {
	// Actions is one of: ["no-op"], ["create"], ["read"], ["update"], ["delete"],
	// ["delete","create"], ["create","delete"]. The last two are a forced replacement, differing
	// only in whether create_before_destroy is set.
	Actions []string `json:"actions"`

	// Before and After are decoded lazily into a generic map, because a resource's attributes
	// are provider-defined and this analyzer only ever looks up a closed list of named paths.
	Before map[string]any `json:"before"`
	After  map[string]any `json:"after"`
}

// destroys reports whether this change destroys the existing object. A forced replacement does,
// which is the whole reason it is not simply an update.
func (c change) destroys() bool {
	for _, a := range c.Actions {
		if a == "delete" {
			return true
		}
	}
	return false
}

// replaces reports whether the object is destroyed and recreated in the same operation.
func (c change) replaces() bool {
	if !c.destroys() {
		return false
	}
	for _, a := range c.Actions {
		if a == "create" {
			return true
		}
	}
	return false
}

// updates reports an in-place update: no destruction, no creation.
func (c change) updates() bool {
	return len(c.Actions) == 1 && c.Actions[0] == "update"
}

// creates reports a pure create.
func (c change) creates() bool {
	return len(c.Actions) == 1 && c.Actions[0] == "create"
}

// decodePlan parses a plan document and reports whether its format is one this build reads.
//
// The error it returns is the text a user sees on a TF009 finding, so it names the version it
// found and the versions it understands rather than saying the file was bad.
func decodePlan(raw []byte) (*plan, error) {
	var p plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("this file is not a Terraform plan document: %w", err)
	}

	if p.FormatVersion == "" {
		return nil, fmt.Errorf("this file has no format_version, so it is not the output of `terraform show -json`")
	}

	if !supportedFormatVersions[p.FormatVersion] {
		return nil, fmt.Errorf("plan format version %q is not one this build reads (it understands %s), so nothing in it can be classified",
			p.FormatVersion, supportedVersionList())
	}

	return &p, nil
}

func supportedVersionList() string {
	out := make([]string, 0, len(supportedFormatVersions))
	for v := range supportedFormatVersions {
		out = append(out, v)
	}
	sort.Strings(out)

	switch len(out) {
	case 0:
		return "none"
	case 1:
		return out[0]
	default:
		return fmt.Sprintf("%s and %s", joinAll(out[:len(out)-1]), out[len(out)-1])
	}
}

func joinAll(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
