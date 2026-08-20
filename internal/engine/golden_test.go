// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

// updateGolden regenerates the verdict snapshot. Run: go test ./internal/engine -update
var updateGolden = flag.Bool("update", false, "rewrite the verdict snapshot")

// snapshotFile records the engine's verdict for every fixture in the repository.
const snapshotFile = "golden/verdicts.txt"

// TestVerdictSnapshot pins what the engine concludes about every fixture, in one reviewable file.
//
// The per-rule fixture tests prove each rule in isolation; this proves the whole pipeline end to
// end and, more usefully, makes any change in behaviour visible as a diff. A scoring tweak that
// quietly moves nine fixtures from B to A shows up here as nine changed lines in a pull request,
// which is exactly the review this project's rules deserve.
//
// The digest column is the strongest part: it is the SHA-256 of the analyzed input, so a fixture
// edited by accident changes its digest and is caught even if its grade happens to stay the same.
func TestVerdictSnapshot(t *testing.T) {
	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	files := provider.NewFake(root)
	e := realEngine()

	var out bytes.Buffer
	out.WriteString("# Engine verdict for every fixture. Regenerate: go test ./internal/engine -update\n")
	out.WriteString("# A change here is a change in what the product tells users. Review it as such.\n\n")

	for _, group := range []string{"kubernetes", "postgres"} {
		cases, err := fixture.Cases(root, group)
		if err != nil {
			t.Fatalf("loading %s fixtures: %v", group, err)
		}

		for _, tc := range cases {
			changed, err := files.ChangedFiles(context.Background(), tc.Ref)
			if err != nil {
				t.Fatalf("resolving %s: %v", tc.Ref, err)
			}

			cert, _ := e.Certify(context.Background(), changed)

			fmt.Fprintf(&out, "%-52s grade=%s gate=%-4s applicable=%-5v findings=%-2d undo=%-2d blockers=%-2d digest=%s\n",
				group+"/"+tc.Name,
				cert.Grade,
				cert.AIGateStatus,
				cert.Applicable,
				len(cert.Findings),
				len(cert.UndoPlan),
				len(cert.Blockers),
				cert.InputDigest[:16],
			)
		}
	}

	path := filepath.Join(root, filepath.FromSlash(snapshotFile))

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating the golden directory: %v", err)
		}
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			t.Fatalf("writing the snapshot: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v\n\nRun: go test ./internal/engine -update", path, err)
	}

	if diff := cmp.Diff(string(want), out.String()); diff != "" {
		t.Errorf("the engine's verdicts changed (-recorded +now):\n%s", diff)
	}
}

// The snapshot must be reproducible, or it would flap in CI and get ignored.
func TestVerdictSnapshotIsStable(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	files := provider.NewFake(root)

	cases, err := fixture.Cases(root, "postgres")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	for _, tc := range cases {
		changed, err := files.ChangedFiles(context.Background(), tc.Ref)
		if err != nil {
			t.Fatalf("resolving %s: %v", tc.Ref, err)
		}

		// A fresh engine each time, so retained state would surface as a difference.
		first, _ := realEngine().Certify(context.Background(), changed)

		for i := 0; i < 10; i++ {
			got, _ := realEngine().Certify(context.Background(), changed)

			if hashCertificate(t, first) != hashCertificate(t, got) {
				t.Fatalf("%s: run %d differed:\n%s", tc.Name, i, cmp.Diff(first, got))
			}
		}
	}
}
