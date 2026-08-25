// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/VIKOIT/reversibility-engine/catalog"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/terraform"
)

func newCatalogCommand(opts Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Inspect and extend the Terraform resource-type catalog",
		Long: "The catalog classifies Terraform resource types as STATEFUL or STATELESS, which is\n" +
			"what turns a destroy in a plan into a verdict.\n\n" +
			"It is compiled into this binary. `revctl check` NEVER fetches it — not on a cache miss,\n" +
			"not when it is old, not ever. Analysis is offline by construction, and the embedded\n" +
			"catalog stays fully functional with no network for the lifetime of the binary.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	cmd.AddCommand(newCatalogShowCommand(opts))
	cmd.AddCommand(newCatalogScanCommand(opts))

	return cmd
}

func newCatalogShowCommand(opts Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the embedded catalog's version and coverage",
		Long: "Print what this build classifies.\n\n" +
			"The coverage number is deliberately the raw one. The AWS provider has on the order of\n" +
			"1,400 resource types and this catalog holds far fewer, because the ones that matter are\n" +
			"the ones whose destruction hurts — but the honest denominator is published rather than a\n" +
			"flattering one.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			providers, err := catalog.TerraformProviders()
			if err != nil {
				return fmt.Errorf("listing embedded catalogs: %w", err)
			}

			for _, p := range providers {
				c, err := terraform.LoadEmbedded(p)
				if err != nil {
					return fmt.Errorf("loading the %s catalog: %w", p, err)
				}

				stateful, stateless := c.Coverage()
				_, _ = fmt.Fprintf(opts.Stdout, "provider %s\n", c.Provider)
				_, _ = fmt.Fprintf(opts.Stdout, "  catalog version  %s\n", c.Version)
				_, _ = fmt.Fprintf(opts.Stdout, "  digest           %s\n", c.Digest())
				_, _ = fmt.Fprintf(opts.Stdout, "  classified       %d types (%d stateful, %d stateless)\n",
					len(c.Entries), stateful, stateless)
			}

			return nil
		},
	}
}

// scanFlags is one invocation of `catalog scan`.
type scanFlags struct {
	provider string
	output   string
}

func newCatalogScanCommand(opts Options) *cobra.Command {
	flags := &scanFlags{}

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Propose catalog entries for types this build does not classify",
		Long: "Read a provider's schema and emit CANDIDATE classifications for every resource type\n" +
			"the catalog does not already carry.\n\n" +
			"This is a maintainer tool and it turns catalog work from research into review. Its\n" +
			"output is a proposal for a human to check and edit — nothing is ever merged\n" +
			"automatically, and no part of `revctl check` depends on this command or on the\n" +
			"toolchain it needs.\n\n" +
			"It shells out to `terraform providers schema -json`, so terraform must be installed\n" +
			"and the provider must already be initialised in the working directory.",
		Args:    cobra.NoArgs,
		Example: "  revctl catalog scan --provider aws --out candidates.yaml",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCatalogScan(cmd, opts, flags)
		},
	}

	cmd.Flags().StringVar(&flags.provider, "provider", "aws",
		"provider short name to scan, matching an embedded catalog")
	cmd.Flags().StringVarP(&flags.output, "out", "o", "",
		"write candidates here instead of stdout")

	return cmd
}

func runCatalogScan(cmd *cobra.Command, opts Options, flags *scanFlags) error {
	if _, err := exec.LookPath("terraform"); err != nil {
		return fmt.Errorf("terraform is not on PATH, and `catalog scan` reads a provider schema through it. "+
			"Install terraform and run `terraform init` in a directory using the provider first: %w", err)
	}

	existing, err := terraform.LoadEmbedded(catalog.TerraformProvider(flags.provider))
	if err != nil {
		return fmt.Errorf("loading the %s catalog: %w", flags.provider, err)
	}

	schema, err := providerSchema(cmd, flags.provider)
	if err != nil {
		return err
	}

	var missing []string
	for name := range schema {
		if _, known := existing.Lookup(name); !known {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)

	rendered := renderCandidates(existing, schema, missing)

	if flags.output == "" {
		_, _ = fmt.Fprint(opts.Stdout, rendered)
	} else if err := os.WriteFile(flags.output, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", flags.output, err)
	}

	_, _ = fmt.Fprintf(opts.Stderr,
		"revctl: %d of %d %s resource types are classified (%.1f%%); %d candidates proposed\n",
		len(existing.Entries), len(schema), flags.provider,
		coveragePercent(len(existing.Entries), len(schema)), len(missing))

	return nil
}

// providerSchema returns every managed resource type the provider declares, with its top-level
// attribute names.
func providerSchema(cmd *cobra.Command, provider string) (map[string][]string, error) {
	out, err := exec.CommandContext(cmd.Context(), "terraform", "providers", "schema", "-json").Output()
	if err != nil {
		return nil, fmt.Errorf("running `terraform providers schema -json`: "+
			"the working directory must be an initialised Terraform configuration using the %s provider: %w", provider, err)
	}

	var doc struct {
		ProviderSchemas map[string]struct {
			ResourceSchemas map[string]struct {
				Block struct {
					Attributes map[string]any `json:"attributes"`
					BlockTypes map[string]any `json:"block_types"`
				} `json:"block"`
			} `json:"resource_schemas"`
		} `json:"provider_schemas"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("reading the provider schema: %w", err)
	}

	schema := map[string][]string{}
	for name, ps := range doc.ProviderSchemas {
		if !strings.Contains(name, "/"+provider) {
			continue
		}
		for resourceType, rs := range ps.ResourceSchemas {
			attrs := make([]string, 0, len(rs.Block.Attributes)+len(rs.Block.BlockTypes))
			for a := range rs.Block.Attributes {
				attrs = append(attrs, a)
			}
			for b := range rs.Block.BlockTypes {
				attrs = append(attrs, b)
			}
			sort.Strings(attrs)
			schema[resourceType] = attrs
		}
	}

	if len(schema) == 0 {
		return nil, errors.New("the provider schema contained no resource types for that provider; check the --provider name and that `terraform init` has run")
	}

	return schema, nil
}

// renderCandidates emits a reviewable proposal, with the evidence that suggested each guess.
//
// The suggestion is derived from the same evidence keys the analyzer uses at runtime, so a
// reviewer is checking the same signal the tool would have acted on. Every candidate is marked
// CANDIDATE and carries an empty evidence field, because an entry without a link fails the build
// — which is what stops this output from being merged unread.
func renderCandidates(existing *terraform.Catalog, schema map[string][]string, missing []string) string {
	var b strings.Builder

	b.WriteString("# CANDIDATES — generated by `revctl catalog scan`. NOT a catalog.\n")
	b.WriteString("#\n")
	b.WriteString("# Every entry below is a proposal for a human to check. Each needs an evidence link\n")
	b.WriteString("# before it can be merged; entries without one fail the build, which is deliberate.\n")
	b.WriteString("#\n")
	fmt.Fprintf(&b, "# Classified already: %d of %d types (%.1f%%).\n",
		len(existing.Entries), len(schema), coveragePercent(len(existing.Entries), len(schema)))
	b.WriteString("\ncandidates:\n")

	for _, t := range missing {
		suggested, why := terraform.SuggestClass(schema[t])

		fmt.Fprintf(&b, "  - type: %s\n", t)
		fmt.Fprintf(&b, "    class: %s\n", suggested)
		b.WriteString("    evidence: \"\"   # REQUIRED — link the provider documentation for this type\n")
		if why != "" {
			fmt.Fprintf(&b, "    # suggested by: %s\n", why)
		} else {
			b.WriteString("    # no stateful evidence in the schema; verify before accepting\n")
		}
	}

	return b.String()
}

func coveragePercent(classified, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(classified) / float64(total) * 100
}
