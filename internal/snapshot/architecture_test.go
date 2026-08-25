// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package snapshot_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The database and cluster drivers may be reached from exactly one package.
//
// This is the test the whole design rests on. The engine does not connect to anything during
// analysis: a separate command writes a file, and the analyzers read the file. That is what
// keeps production credentials out of CI, keeps a certificate byte-identical between runs, and
// keeps the analyzers pure functions over a changeset — and none of those survive a single
// import statement in the wrong package.
//
// It lives here rather than in a repository-wide test package because it is the boundary this
// package defines. It checks transitive imports, not just direct ones: a driver reached through
// one hop is reached.
var forbiddenDrivers = []string{
	"github.com/jackc/pgx",
	"k8s.io/client-go",
	"k8s.io/api",
	"k8s.io/apimachinery",
}

// Packages that must never reach a driver, and the reason each one must not.
var driverFreePackages = map[string]string{
	"github.com/VIKOIT/reversibility-engine/internal/domain": "domain imports nothing outside the standard library; that is rule one of the dependency rules",

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/...": "analyzers are pure functions over a changeset — no network, no disk, no database",

	"github.com/VIKOIT/reversibility-engine/internal/snapshot": "the snapshot types are read by the engine, so linking a driver here would put a database client inside every analysis",

	"github.com/VIKOIT/reversibility-engine/internal/engine": "the engine never connects to anything; a snapshot reaches it as a file that has already been read",
}

func TestDriversAreReachableFromTheCollectorOnly(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go tool is not on PATH")
	}

	for pkg, why := range driverFreePackages {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()

			deps := dependenciesOf(t, pkg)

			for _, dep := range deps {
				for _, driver := range forbiddenDrivers {
					if dep == driver || strings.HasPrefix(dep, driver+"/") {
						t.Errorf("%s reaches %s.\n\n%s.\n\n"+
							"A driver belongs only in internal/snapshot/collect, which the CLI's snapshot "+
							"command calls and analysis never does.", pkg, dep, why)
					}
				}
			}
		})
	}
}

// The collector is the one place a driver is allowed, and it has to actually be there — a test
// that only forbids things would keep passing if the collector quietly stopped working.
func TestCollectorDoesReachTheDrivers(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go tool is not on PATH")
	}

	deps := dependenciesOf(t, "github.com/VIKOIT/reversibility-engine/internal/snapshot/collect")

	for _, driver := range []string{"github.com/jackc/pgx", "k8s.io/client-go"} {
		found := false
		for _, dep := range deps {
			if dep == driver || strings.HasPrefix(dep, driver+"/") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the collector does not import %s; either it stopped collecting or the guard above is now vacuous", driver)
		}
	}
}

// dependenciesOf returns every package a pattern transitively imports.
func dependenciesOf(t *testing.T, pattern string) []string {
	t.Helper()

	cmd := exec.Command("go", "list", "-deps", pattern)
	cmd.Dir = ".."

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pattern, err, out)
	}

	return strings.Fields(string(out))
}
