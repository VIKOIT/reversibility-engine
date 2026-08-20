// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/VIKOIT/reversibility-engine/pkg/certificate"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the certificate schema version",
		Long: "Print the version of the certificate schema this build emits.\n\n" +
			"The schema version is what downstream consumers pin against; it is bumped only on a\n" +
			"breaking field change, so it is more useful to a pipeline than a build number.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "certificate schema %s\n", certificate.SchemaVersion); err != nil {
				return fmt.Errorf("writing the version: %w", err)
			}
			return nil
		},
	}
}
