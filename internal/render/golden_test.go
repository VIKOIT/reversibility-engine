package render_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
	"github.com/VIKOIT/reversibility-engine/internal/render"
)

// update regenerates the golden files. Run: go test ./internal/render -update
//
// Regenerating is deliberately a separate, explicit action. A test that silently rewrote its own
// expectations would turn every rendering regression into a passing test.
var update = flag.Bool("update", false, "rewrite the golden files")

// goldenScenarios cover every grade and both analyzers, because the renderers branch on grade,
// on whether a plan exists, and on whether the changeset was applicable at all.
var goldenScenarios = []struct {
	name string
	ref  domain.ChangeRef
}{
	{"grade-a-postgres", "postgres/PG024_create_index_concurrently"},
	{"grade-a-kubernetes", "kubernetes/K8S015_image_changed_digest_pinned"},
	{"grade-b-postgres", "postgres/PG012_rename"},
	{"grade-c-missing-down", "postgres/DOWN001_missing"},
	{"grade-f-irreversible", "postgres/PG001_drop_table"},
	{"grade-f-unknown", "postgres/PG027_unparsed_or_unrecognized"},
	{"grade-f-kubernetes", "kubernetes/K8S006_namespace_or_crd_removed"},
}

func fileExtension(format string) string {
	switch format {
	case render.FormatJSON:
		return "json"
	case render.FormatMarkdown:
		return "md"
	default:
		return "sarif.json"
	}
}

func certificateFor(t *testing.T, ref domain.ChangeRef) domain.ReversibilityCertificate {
	t.Helper()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	files, err := provider.NewFake(root).ChangedFiles(context.Background(), ref)
	if err != nil {
		t.Fatalf("resolving %s: %v", ref, err)
	}

	eng := engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()})

	cert, _ := eng.Certify(context.Background(), files)
	return cert
}

// notApplicableCertificate is the one scenario no fixture produces: a changeset the engine has
// no opinion on. The renderers must say so rather than presenting a bare A.
func notApplicableCertificate(t *testing.T) domain.ReversibilityCertificate {
	t.Helper()

	eng := engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()})

	cert, err := eng.Certify(context.Background(), []domain.ChangedFile{
		{Path: "README.md", Status: domain.StatusModified, Current: []byte("# hello")},
	})
	if err != nil {
		t.Fatalf("Certify: %v", err)
	}
	return cert
}

func goldenPath(t *testing.T, name, format string) string {
	t.Helper()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}
	return filepath.Join(root, "golden", name+"."+fileExtension(format))
}

func assertGolden(t *testing.T, name, format string, got []byte) {
	t.Helper()

	path := goldenPath(t, name, format)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating golden directory: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing golden file: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s: %v\n\nRun: go test ./internal/render -update", path, err)
	}

	// Golden files are stored with LF endings and .gitattributes enforces that, so a byte
	// comparison is meaningful on every platform.
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("output does not match %s (-want +got):\n%s", path, diff)
	}
}

// TestGolden pins the exact bytes each renderer produces.
//
// These files are the contract with everything downstream: a PR comment, a code-scanning upload,
// a merge gate parsing JSON. A change to any of them should be a deliberate, reviewable diff.
func TestGolden(t *testing.T) {
	for _, scenario := range goldenScenarios {
		for _, format := range render.Formats() {
			t.Run(scenario.name+"/"+format, func(t *testing.T) {
				renderer, err := render.For(format)
				if err != nil {
					t.Fatalf("render.For(%q): %v", format, err)
				}

				var buf bytes.Buffer
				if err := renderer.Render(&buf, certificateFor(t, scenario.ref)); err != nil {
					t.Fatalf("Render: %v", err)
				}

				assertGolden(t, scenario.name, format, buf.Bytes())
			})
		}
	}

	for _, format := range render.Formats() {
		t.Run("not-applicable/"+format, func(t *testing.T) {
			renderer, err := render.For(format)
			if err != nil {
				t.Fatalf("render.For(%q): %v", format, err)
			}

			var buf bytes.Buffer
			if err := renderer.Render(&buf, notApplicableCertificate(t)); err != nil {
				t.Fatalf("Render: %v", err)
			}

			assertGolden(t, "not-applicable", format, buf.Bytes())
		})
	}
}

// Rendering must be a pure function of the certificate. Anything that varied between calls —
// map iteration in the SARIF rule set, a timestamp, a build stamp — would show up here.
func TestRenderersAreDeterministic(t *testing.T) {
	t.Parallel()

	for _, scenario := range goldenScenarios {
		cert := certificateFor(t, scenario.ref)

		for _, format := range render.Formats() {
			t.Run(scenario.name+"/"+format, func(t *testing.T) {
				t.Parallel()

				renderer, err := render.For(format)
				if err != nil {
					t.Fatalf("render.For: %v", err)
				}

				var first bytes.Buffer
				if err := renderer.Render(&first, cert); err != nil {
					t.Fatalf("Render: %v", err)
				}

				for i := 0; i < 100; i++ {
					var got bytes.Buffer
					if err := renderer.Render(&got, cert); err != nil {
						t.Fatalf("Render: %v", err)
					}
					if !bytes.Equal(first.Bytes(), got.Bytes()) {
						t.Fatalf("run %d differed:\n%s", i, cmp.Diff(first.String(), got.String()))
					}
				}
			})
		}
	}
}

// Both machine formats must be valid JSON. A malformed SARIF upload is rejected silently by code
// scanning, which would look exactly like "no findings".
func TestMachineFormatsAreValidJSON(t *testing.T) {
	t.Parallel()

	for _, scenario := range goldenScenarios {
		cert := certificateFor(t, scenario.ref)

		for _, format := range []string{render.FormatJSON, render.FormatSARIF} {
			t.Run(scenario.name+"/"+format, func(t *testing.T) {
				t.Parallel()

				renderer, _ := render.For(format)

				var buf bytes.Buffer
				if err := renderer.Render(&buf, cert); err != nil {
					t.Fatalf("Render: %v", err)
				}

				var decoded any
				if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
					t.Errorf("output is not valid JSON: %v\n%s", err, buf.String())
				}
			})
		}
	}
}
