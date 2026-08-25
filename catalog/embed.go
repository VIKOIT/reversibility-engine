// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package catalog embeds the resource-type catalogs the Terraform analyzer classifies against.
//
// The data lives at catalog/terraform/*.yaml — outside internal/ — because contributing a
// resource type is the growth loop this analyzer depends on, and a contributor has to be able to
// find the file. A path under internal/ would say "implementation detail" about the one file
// most likely to receive an outside pull request.
//
// It is embedded rather than read from disk so the tool works with no network and no data
// directory, for the lifetime of the binary. Analysis NEVER fetches a catalog: `revctl catalog
// update` is a separate, explicit, user-initiated command, and `revctl check` has no code path
// that reaches the network at all.
package catalog

import (
	"embed"
	"fmt"
	"io/fs"
)

// files holds every catalog shipped with this build.
//
//go:embed terraform/*.yaml
var files embed.FS

// TerraformProvider names a catalog by its Terraform provider short name, such as "aws".
type TerraformProvider string

// AWS is the only provider catalog seeded so far.
const AWS TerraformProvider = "aws"

// Terraform returns the embedded catalog for a provider.
func Terraform(provider TerraformProvider) ([]byte, error) {
	raw, err := files.ReadFile(fmt.Sprintf("terraform/%s.yaml", provider))
	if err != nil {
		return nil, fmt.Errorf("no embedded catalog for provider %q: %w", provider, err)
	}
	return raw, nil
}

// TerraformProviders lists every provider catalog in this build, so a caller can load them all
// without a second list to keep in step with the directory.
func TerraformProviders() ([]TerraformProvider, error) {
	entries, err := fs.Glob(files, "terraform/*.yaml")
	if err != nil {
		return nil, fmt.Errorf("listing embedded catalogs: %w", err)
	}

	out := make([]TerraformProvider, 0, len(entries))
	for _, e := range entries {
		name := e[len("terraform/") : len(e)-len(".yaml")]
		out = append(out, TerraformProvider(name))
	}
	return out, nil
}
