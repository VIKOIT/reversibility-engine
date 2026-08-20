// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"context"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// FileProvider resolves a change reference into the files it touched.
//
// This is the only interface in the engine permitted to perform I/O. Everything downstream of
// it operates on bytes already in memory, which is what keeps the analyzers deterministic and
// their rule tables testable from fixtures.
type FileProvider interface {
	// ChangedFiles returns the files touched by ref, with both sides of each change populated.
	//
	// The result must be sorted by path. Callers hash it into the certificate's InputDigest,
	// and an unstable order would produce a different digest for identical input.
	ChangedFiles(ctx context.Context, ref domain.ChangeRef) ([]domain.ChangedFile, error)
}
