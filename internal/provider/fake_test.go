package provider_test

import (
	"context"
	"sort"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}
	return root
}

// The migrations/ shape is what a migration pull request looks like: every file is new.
func TestChangedFilesMigrationShape(t *testing.T) {
	t.Parallel()

	files := provider.NewFake(fixtureRoot(t))

	got, err := files.ChangedFiles(context.Background(), provider.FixtureRef("postgres", "PG001_drop_table"))
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	want := []string{
		"migrations/0001_drop_legacy_orders.down.sql",
		"migrations/0001_drop_legacy_orders.up.sql",
	}
	assertPaths(t, got, want)

	for _, f := range got {
		if f.Status != domain.StatusAdded {
			t.Errorf("%s: status %q, want ADDED", f.Path, f.Status)
		}
		if f.Previous != nil {
			t.Errorf("%s: an added file must have no previous content", f.Path)
		}
		if len(f.Current) == 0 {
			t.Errorf("%s: current content is empty", f.Path)
		}
	}
}

// The old//new/ shape must classify each path by which side it appears on.
func TestChangedFilesTreePairShape(t *testing.T) {
	t.Parallel()

	files := provider.NewFake(fixtureRoot(t))

	got, err := files.ChangedFiles(context.Background(), provider.FixtureRef("kubernetes", "K8S003_pvc_removed_reclaim_delete"))
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	byPath := map[string]domain.ChangedFile{}
	for _, f := range got {
		byPath[f.Path] = f
	}

	pvc, ok := byPath["pvc.yaml"]
	if !ok {
		t.Fatalf("pvc.yaml missing from changeset; got %v", paths(got))
	}
	if pvc.Status != domain.StatusRemoved {
		t.Errorf("pvc.yaml: status %q, want REMOVED", pvc.Status)
	}
	if pvc.Current != nil {
		t.Errorf("pvc.yaml: a removed file must have no current content")
	}
	if len(pvc.Previous) == 0 {
		t.Errorf("pvc.yaml: a removed file must carry its previous content, or nothing can classify it")
	}

	// The StorageClass is unchanged, but K8S003's verdict depends on its reclaimPolicy. If the
	// provider dropped unchanged files, the rule would be unimplementable.
	sc, ok := byPath["storageclass.yaml"]
	if !ok {
		t.Fatalf("storageclass.yaml missing; unchanged context files must still be supplied")
	}
	if sc.Status != domain.StatusModified {
		t.Errorf("storageclass.yaml: status %q, want MODIFIED", sc.Status)
	}
}

func TestChangedFilesDetectsAdditions(t *testing.T) {
	t.Parallel()

	files := provider.NewFake(fixtureRoot(t))

	got, err := files.ChangedFiles(context.Background(), provider.FixtureRef("kubernetes", "K8S013_new_workload"))
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	assertPaths(t, got, []string{"deployment.yaml"})
	if got[0].Status != domain.StatusAdded {
		t.Errorf("deployment.yaml: status %q, want ADDED", got[0].Status)
	}
}

// Bookkeeping files are not part of any changeset and must never reach an analyzer.
func TestChangedFilesSkipsBookkeeping(t *testing.T) {
	t.Parallel()

	files := provider.NewFake(fixtureRoot(t))

	got, err := files.ChangedFiles(context.Background(), provider.FixtureRef("kubernetes", "K8S006_namespace_or_crd_removed"))
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	for _, f := range got {
		if f.Path == ".gitkeep" || f.Path == "expected.json" {
			t.Errorf("%s leaked into the changeset", f.Path)
		}
	}
	assertPaths(t, got, []string{"crd.yaml", "namespace.yaml"})
}

// The interface promises a stable order because InputDigest is computed from it.
func TestChangedFilesIsSorted(t *testing.T) {
	t.Parallel()

	files := provider.NewFake(fixtureRoot(t))

	for _, ref := range []domain.ChangeRef{
		provider.FixtureRef("postgres", "PG016_drop_view_function_trigger"),
		provider.FixtureRef("kubernetes", "K8S009_configmap_removed_still_referenced"),
	} {
		got, err := files.ChangedFiles(context.Background(), ref)
		if err != nil {
			t.Fatalf("ChangedFiles(%q): %v", ref, err)
		}

		p := paths(got)
		if !sort.StringsAreSorted(p) {
			t.Errorf("ChangedFiles(%q) returned unsorted paths: %v", ref, p)
		}
	}
}

// The directory form migrations/NNN/up.sql must resolve as readily as the flat form.
func TestChangedFilesDirectoryMigrationForm(t *testing.T) {
	t.Parallel()

	files := provider.NewFake(fixtureRoot(t))

	got, err := files.ChangedFiles(context.Background(), provider.FixtureRef("postgres", "DOWN004_directory_form"))
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	assertPaths(t, got, []string{"migrations/0001/down.sql", "migrations/0001/up.sql"})
}

func TestChangedFilesMissingFixture(t *testing.T) {
	t.Parallel()

	files := provider.NewFake(fixtureRoot(t))

	if _, err := files.ChangedFiles(context.Background(), "postgres/PG999_does_not_exist"); err == nil {
		t.Fatalf("expected an error for a missing fixture, got nil")
	}
}

func TestChangedFilesRespectsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	files := provider.NewFake(fixtureRoot(t))
	if _, err := files.ChangedFiles(ctx, provider.FixtureRef("postgres", "PG001_drop_table")); err == nil {
		t.Fatalf("expected an error for a cancelled context, got nil")
	}
}

func paths(files []domain.ChangedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func assertPaths(t *testing.T, got []domain.ChangedFile, want []string) {
	t.Helper()

	gotPaths := paths(got)
	if len(gotPaths) != len(want) {
		t.Fatalf("changeset paths = %v, want %v", gotPaths, want)
	}
	for i := range want {
		if gotPaths[i] != want[i] {
			t.Fatalf("changeset paths = %v, want %v", gotPaths, want)
		}
	}
}
