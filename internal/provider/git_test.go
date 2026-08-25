// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

// The git provider is tested against real repositories rather than a stubbed command runner.
// What is being asserted is agreement with git — how it names a rename, what a three-dot range
// contains, which commits a shallow clone is missing — and a fake would only assert agreement
// with this file's own idea of git.

// repo is a throwaway git repository under t.TempDir().
type repo struct {
	t   *testing.T
	dir string
}

// requireGit skips the caller when git is not installed. The suite proves the provider agrees
// with git, so without git there is nothing to prove — and failing would only report the
// developer's machine.
func requireGit(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	requireGit(t)

	r := &repo{t: t, dir: t.TempDir()}

	r.run("init", "--quiet", ".")
	// Naming the branch this way rather than with --initial-branch keeps the suite working on
	// git versions older than 2.28.
	r.run("symbolic-ref", "HEAD", "refs/heads/main")
	r.run("config", "user.name", "Reversibility Test")
	r.run("config", "user.email", "test@example.invalid")
	r.run("config", "commit.gpgsign", "false")

	return r
}

// run executes git in the repository and fails the test if it errors.
func (r *repo) run(args ...string) string {
	r.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// write puts content at a repository-relative path, creating parent directories.
func (r *repo) write(path, content string) {
	r.t.Helper()

	full := filepath.Join(r.dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatalf("creating %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatalf("writing %s: %v", path, err)
	}
}

func (r *repo) remove(path string) {
	r.t.Helper()

	if err := os.Remove(filepath.Join(r.dir, filepath.FromSlash(path))); err != nil {
		r.t.Fatalf("removing %s: %v", path, err)
	}
}

// commit stages everything and commits it, returning the new commit's SHA.
func (r *repo) commit(message string) string {
	r.t.Helper()

	r.run("add", "--all")
	r.run("commit", "--quiet", "--message", message)
	return r.run("rev-parse", "HEAD")
}

// changedFiles resolves the changeset the provider reports for this repository.
func (r *repo) changedFiles(t *testing.T, opts provider.GitOptions, include provider.Include) ([]domain.ChangedFile, error) {
	t.Helper()

	opts.Dir = r.dir
	p, err := provider.NewGit(opts, include)
	if err != nil {
		return nil, err
	}
	return p.ChangedFiles(context.Background(), "")
}

func onlySQL(path string) bool { return strings.HasSuffix(path, ".sql") }

func TestGitResolvesAChangeset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// base defaults to HEAD~1, which is the shape of most of these cases: one commit on top
		// of a starting point.
		base    string
		setup   func(r *repo)
		paths   []string
		include provider.Include
		want    []domain.ChangedFile
	}{
		{
			name: "added file",
			setup: func(r *repo) {
				r.write("README.md", "start\n")
				r.commit("base")
				r.write("migrations/0001_add.sql", "CREATE TABLE t (id bigint);\n")
				r.commit("add a migration")
			},
			want: []domain.ChangedFile{{
				Path:    "migrations/0001_add.sql",
				Status:  domain.StatusAdded,
				Current: []byte("CREATE TABLE t (id bigint);\n"),
			}},
		},
		{
			name: "modified file carries both sides",
			setup: func(r *repo) {
				r.write("k8s/deployment.yaml", "replicas: 3\n")
				r.commit("base")
				r.write("k8s/deployment.yaml", "replicas: 1\n")
				r.commit("scale down")
			},
			want: []domain.ChangedFile{{
				Path:     "k8s/deployment.yaml",
				Status:   domain.StatusModified,
				Previous: []byte("replicas: 3\n"),
				Current:  []byte("replicas: 1\n"),
			}},
		},
		{
			name: "deleted file keeps the previous side",
			setup: func(r *repo) {
				r.write("k8s/pvc.yaml", "kind: PersistentVolumeClaim\n")
				r.commit("base")
				r.remove("k8s/pvc.yaml")
				r.commit("delete the claim")
			},
			want: []domain.ChangedFile{{
				Path:     "k8s/pvc.yaml",
				Status:   domain.StatusRemoved,
				Previous: []byte("kind: PersistentVolumeClaim\n"),
			}},
		},
		{
			// A rename is a delete plus an add. The Kubernetes rules compare whole objects, so
			// they have to see the removal to ask what still depends on it.
			name: "rename becomes a delete and an add",
			setup: func(r *repo) {
				r.write("k8s/old-name.yaml", "kind: ConfigMap\nmetadata:\n  name: settings\n")
				r.commit("base")
				r.run("mv", "k8s/old-name.yaml", "k8s/new-name.yaml")
				r.commit("rename the manifest")
			},
			want: []domain.ChangedFile{
				{
					Path:    "k8s/new-name.yaml",
					Status:  domain.StatusAdded,
					Current: []byte("kind: ConfigMap\nmetadata:\n  name: settings\n"),
				},
				{
					Path:     "k8s/old-name.yaml",
					Status:   domain.StatusRemoved,
					Previous: []byte("kind: ConfigMap\nmetadata:\n  name: settings\n"),
				},
			},
		},
		{
			name: "path arguments scope the analysis to a subtree",
			setup: func(r *repo) {
				r.write("migrations/0001.sql", "SELECT 1;\n")
				r.write("k8s/deployment.yaml", "replicas: 3\n")
				r.commit("base")
				r.write("migrations/0002.sql", "DROP TABLE t;\n")
				r.write("k8s/deployment.yaml", "replicas: 9\n")
				r.commit("change both trees")
			},
			paths: []string{"migrations"},
			want: []domain.ChangedFile{
				{
					Path:    "migrations/0002.sql",
					Status:  domain.StatusAdded,
					Current: []byte("DROP TABLE t;\n"),
				},
				{
					// A sibling the change did not touch, returned as context.
					Path:     "migrations/0001.sql",
					Status:   domain.StatusModified,
					Previous: []byte("SELECT 1;\n"),
					Current:  []byte("SELECT 1;\n"),
				},
			},
		},
		{
			// The certificate has to describe the refs it names. A developer with uncommitted
			// edits must get the same answer as CI reading the same two commits.
			name: "a dirty working tree is invisible",
			setup: func(r *repo) {
				r.write("migrations/0001.sql", "SELECT 1;\n")
				r.commit("base")
				r.write("migrations/0002.sql", "ALTER TABLE t ADD COLUMN c text;\n")
				r.commit("add a column")

				// Neither of these is committed, so neither may appear.
				r.write("migrations/0002.sql", "DROP DATABASE production;\n")
				r.write("migrations/0003_untracked.sql", "DROP TABLE t;\n")
			},
			include: onlySQL,
			want: []domain.ChangedFile{
				{
					Path:    "migrations/0002.sql",
					Status:  domain.StatusAdded,
					Current: []byte("ALTER TABLE t ADD COLUMN c text;\n"),
				},
				{
					Path:     "migrations/0001.sql",
					Status:   domain.StatusModified,
					Previous: []byte("SELECT 1;\n"),
					Current:  []byte("SELECT 1;\n"),
				},
			},
		},
		{
			// K8S003 needs the StorageClass nobody edited; without it the rule can only answer
			// UNKNOWN. The GitHub provider supplies these, and the CLI has to agree with it.
			name: "unchanged siblings come back as context",
			setup: func(r *repo) {
				r.write("k8s/storageclass.yaml", "kind: StorageClass\nreclaimPolicy: Delete\n")
				r.write("k8s/pvc.yaml", "kind: PersistentVolumeClaim\n")
				r.commit("base")
				r.remove("k8s/pvc.yaml")
				r.commit("delete the claim")
			},
			want: []domain.ChangedFile{
				{
					Path:     "k8s/pvc.yaml",
					Status:   domain.StatusRemoved,
					Previous: []byte("kind: PersistentVolumeClaim\n"),
				},
				{
					Path:     "k8s/storageclass.yaml",
					Status:   domain.StatusModified,
					Previous: []byte("kind: StorageClass\nreclaimPolicy: Delete\n"),
					Current:  []byte("kind: StorageClass\nreclaimPolicy: Delete\n"),
				},
			},
		},
		{
			name: "the include predicate keeps unsupported files out",
			setup: func(r *repo) {
				r.write("notes.txt", "before\n")
				r.commit("base")
				r.write("notes.txt", "after\n")
				r.write("migrations/0001.sql", "SELECT 1;\n")
				r.commit("change both")
			},
			include: onlySQL,
			want: []domain.ChangedFile{{
				Path:    "migrations/0001.sql",
				Status:  domain.StatusAdded,
				Current: []byte("SELECT 1;\n"),
			}},
		},
		{
			// Three-dot semantics: the comparison is against the merge base, so commits added
			// to base after the branch diverged are not reported as reversed.
			name: "the comparison uses the merge base",
			base: "main",
			setup: func(r *repo) {
				r.write("migrations/0001.sql", "SELECT 1;\n")
				r.commit("base")

				r.run("checkout", "--quiet", "-b", "feature")
				r.write("migrations/0002_feature.sql", "CREATE INDEX CONCURRENTLY i ON t (a);\n")
				r.commit("feature work")

				r.run("checkout", "--quiet", "main")
				r.write("migrations/0003_other.sql", "SELECT 2;\n")
				r.commit("unrelated work on main")

				r.run("checkout", "--quiet", "feature")
			},
			include: onlySQL,
			want: []domain.ChangedFile{
				{
					Path:    "migrations/0002_feature.sql",
					Status:  domain.StatusAdded,
					Current: []byte("CREATE INDEX CONCURRENTLY i ON t (a);\n"),
				},
				{
					Path:     "migrations/0001.sql",
					Status:   domain.StatusModified,
					Previous: []byte("SELECT 1;\n"),
					Current:  []byte("SELECT 1;\n"),
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRepo(t)
			tc.setup(r)

			base := tc.base
			if base == "" {
				base = "HEAD~1"
			}

			got, err := r.changedFiles(t, provider.GitOptions{Base: base, Paths: tc.paths}, tc.include)
			if err != nil {
				t.Fatalf("ChangedFiles: %v", err)
			}

			want := append([]domain.ChangedFile(nil), tc.want...)
			sortForComparison(want)

			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("changed files (-want +got):\n%s", diff)
			}
		})
	}
}

// sortForComparison puts expectations in the order the interface promises, so that a test case
// can be written in whatever order reads best.
func sortForComparison(files []domain.ChangedFile) {
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j].Path < files[j-1].Path; j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
}

func TestGitHeadDefaultsToHEAD(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("migrations/0001.sql", "SELECT 1;\n")
	r.commit("base")
	r.write("migrations/0002.sql", "SELECT 2;\n")
	head := r.commit("second")

	withDefault, err := r.changedFiles(t, provider.GitOptions{Base: "HEAD~1"}, onlySQL)
	if err != nil {
		t.Fatalf("ChangedFiles with the default head: %v", err)
	}

	explicit, err := r.changedFiles(t, provider.GitOptions{Base: "HEAD~1", Head: head}, onlySQL)
	if err != nil {
		t.Fatalf("ChangedFiles with an explicit head: %v", err)
	}

	if diff := cmp.Diff(withDefault, explicit); diff != "" {
		t.Errorf("an omitted --head differs from HEAD (-default +explicit):\n%s", diff)
	}
}

func TestGitFailsLoudly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(t *testing.T) (dir string, opts provider.GitOptions)
		want    error
		mustSay string
	}{
		{
			name: "not a repository",
			setup: func(t *testing.T) (string, provider.GitOptions) {
				requireGit(t)
				return t.TempDir(), provider.GitOptions{Base: "HEAD~1"}
			},
			want: provider.ErrNotARepository,
		},
		{
			name: "unknown ref",
			setup: func(t *testing.T) (string, provider.GitOptions) {
				r := newRepo(t)
				r.write("a.sql", "SELECT 1;\n")
				r.commit("base")
				return r.dir, provider.GitOptions{Base: "origin/nonexistent"}
			},
			want:    provider.ErrUnknownRef,
			mustSay: "origin/nonexistent",
		},
		{
			// A branch and a tag sharing a name. git picks one and warns; certifying its
			// preference would compare something the user never asked for.
			name: "ambiguous ref",
			setup: func(t *testing.T) (string, provider.GitOptions) {
				r := newRepo(t)
				r.write("a.sql", "SELECT 1;\n")
				r.commit("base")
				r.write("b.sql", "SELECT 2;\n")
				r.commit("second")
				r.run("branch", "release", "HEAD~1")
				r.run("tag", "release", "HEAD")
				return r.dir, provider.GitOptions{Base: "release"}
			},
			want:    provider.ErrAmbiguousRef,
			mustSay: "refs/heads/release",
		},
		{
			// The most common CI failure by a wide margin, which is why the message has to name
			// the fix rather than repeat git's wording.
			name: "shallow clone missing the base commit",
			setup: func(t *testing.T) (string, provider.GitOptions) {
				origin := newRepo(t)
				origin.write("a.sql", "SELECT 1;\n")
				origin.commit("base")
				origin.write("b.sql", "SELECT 2;\n")
				origin.commit("second")
				missing := origin.run("rev-parse", "HEAD~1")

				clone := filepath.Join(t.TempDir(), "shallow")
				cloneShallow(t, origin.dir, clone)

				return clone, provider.GitOptions{Base: missing}
			},
			want:    provider.ErrShallowClone,
			mustSay: "fetch-depth: 0",
		},
		{
			name: "a ref that names a tree rather than a commit",
			setup: func(t *testing.T) (string, provider.GitOptions) {
				r := newRepo(t)
				r.write("a.sql", "SELECT 1;\n")
				r.commit("base")
				return r.dir, provider.GitOptions{Base: "HEAD^{tree}"}
			},
			want: provider.ErrUnknownRef,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir, opts := tc.setup(t)
			opts.Dir = dir

			p, err := provider.NewGit(opts, nil)
			if err != nil {
				t.Fatalf("NewGit: %v", err)
			}

			files, err := p.ChangedFiles(context.Background(), "")
			if err == nil {
				t.Fatalf("ChangedFiles succeeded with %d files, want %v", len(files), tc.want)
			}
			if files != nil {
				t.Errorf("a failed fetch returned %d files; a partial changeset must never reach the engine", len(files))
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want one wrapping %v", err, tc.want)
			}
			if tc.mustSay != "" && !strings.Contains(err.Error(), tc.mustSay) {
				t.Errorf("error %q does not mention %q", err, tc.mustSay)
			}
		})
	}
}

// cloneShallow makes a depth-1 clone, which is what actions/checkout produces by default.
func cloneShallow(t *testing.T, origin, dest string) {
	t.Helper()

	// A local path clone would hardlink the whole object database and ignore --depth, so the
	// origin is addressed as a URL.
	url := "file://" + filepath.ToSlash(origin)

	cmd := exec.Command("git", "clone", "--quiet", "--depth", "1", url, dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --depth 1: %v\n%s", err, out)
	}
}

func TestNewGitRequiresABaseRef(t *testing.T) {
	t.Parallel()

	if _, err := provider.NewGit(provider.GitOptions{Base: "   "}, nil); !errors.Is(err, domain.ErrProviderFailed) {
		t.Errorf("NewGit with a blank base = %v, want a provider failure", err)
	}
}

func TestGitHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("a.sql", "SELECT 1;\n")
	r.commit("base")

	p, err := provider.NewGit(provider.GitOptions{Dir: r.dir, Base: "HEAD"}, nil)
	if err != nil {
		t.Fatalf("NewGit: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := p.ChangedFiles(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("ChangedFiles on a cancelled context = %v, want context.Canceled", err)
	}
}

// A changeset resolved from git must be usable as an engine input, which means both sides
// populated in the shape the change model requires.
func TestGitProducesWellFormedChangedFiles(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("k8s/a.yaml", "kind: ConfigMap\n")
	r.write("k8s/b.yaml", "kind: Service\n")
	r.commit("base")
	r.write("k8s/a.yaml", "kind: ConfigMap\ndata: {}\n")
	r.remove("k8s/b.yaml")
	r.write("k8s/c.yaml", "kind: Secret\n")
	r.commit("change everything")

	files, err := r.changedFiles(t, provider.GitOptions{Base: "HEAD~1"}, nil)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	for _, f := range files {
		if !f.Status.Valid() {
			t.Errorf("%s: status %q is not a defined status", f.Path, f.Status)
		}
		if f.Path != filepath.ToSlash(f.Path) {
			t.Errorf("%s: path is not slash-separated", f.Path)
		}

		switch f.Status {
		case domain.StatusAdded:
			if f.Previous != nil || f.Current == nil {
				t.Errorf("%s: ADDED must carry only the new side", f.Path)
			}
		case domain.StatusRemoved:
			if f.Current != nil || f.Previous == nil {
				t.Errorf("%s: REMOVED must carry only the previous side", f.Path)
			}
		case domain.StatusModified:
			if f.Previous == nil || f.Current == nil {
				t.Errorf("%s: MODIFIED must carry both sides", f.Path)
			}
		case domain.StatusRenamed:
			t.Errorf("%s: a rename must be reported as a delete plus an add", f.Path)
		}
	}
}
