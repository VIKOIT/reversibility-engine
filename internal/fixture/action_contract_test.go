// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package fixture_test

// The contract between action.yml and the scripts it runs, checked mechanically.
//
// action.yml is what every consumer of this action configures against, and until this file
// existed nothing validated it at all: actionlint reads workflows, not composite action
// definitions, so the file had no schema check and no test that a declared input reached
// anything or a declared output came from anywhere.
//
// The cost of that was measured rather than imagined. `require-full-coverage` was declared as an
// input with a long description telling the reader it is deprecated — and it was never passed to
// any script. certify.sh's deprecation warning could not fire because the value never arrived,
// and when the corrupted condition inside certify.sh was repaired in v1.2.2 the warning still
// could not fire, because the missing wiring was one layer further out and nobody was looking
// there. Two layers of the same silence, and both of them reviewed.
//
// So: an input a consumer can set must reach the code, and an output a consumer can read must
// come from the code. Neither direction is left to review.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// actionDefinition is the subset of action.yml this test constrains. The `runs` block is kept as
// raw YAML rather than modelled: the test asks whether a name is referenced anywhere inside it,
// and modelling composite steps would be a second definition of GitHub's schema to get wrong.
type actionDefinition struct {
	Inputs  map[string]yaml.Node `yaml:"inputs"`
	Outputs map[string]struct {
		Value string `yaml:"value"`
	} `yaml:"outputs"`
	Runs yaml.Node `yaml:"runs"`
}

// repoRoot is the one in table_test.go, in this same package. A second implementation of "where
// is the repository root" is precisely what §13 forbids: two producers of one value, agreeing
// until they do not.

func loadAction(t *testing.T) (actionDefinition, string, map[string]string) {
	t.Helper()

	root := repoRoot(t)

	raw, err := os.ReadFile(filepath.Join(root, "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	var def actionDefinition
	if err := yaml.Unmarshal(raw, &def); err != nil {
		t.Fatalf("parse action.yml: %v", err)
	}

	// The `runs` block re-encoded, so "is this name referenced" is a search over the steps only
	// and cannot accidentally match the input's own declaration or its description prose.
	runs, err := yaml.Marshal(&def.Runs)
	if err != nil {
		t.Fatalf("re-encode runs: %v", err)
	}

	scripts := map[string]string{}
	dir := filepath.Join(root, "action")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read action/: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		scripts[e.Name()] = string(body)
	}

	if len(scripts) == 0 {
		t.Fatal("no scripts found under action/; this test is looking in the wrong place")
	}

	return def, string(runs), scripts
}

// envName is the environment variable a composite action conventionally maps an input to:
// `fail-on-gate` becomes INPUT_FAIL_ON_GATE.
func envName(input string) string {
	return "INPUT_" + strings.ToUpper(strings.ReplaceAll(input, "-", "_"))
}

func TestActionInputsAreWiredAndRead(t *testing.T) {
	def, runs, scripts := loadAction(t)

	if len(def.Inputs) == 0 {
		t.Fatal("action.yml declares no inputs; this test is not reading what it thinks it is")
	}

	for name := range def.Inputs {
		t.Run(name, func(t *testing.T) {
			// Layer one: the input reaches the steps at all. A declared input that no step
			// mentions is a switch on the dashboard wired to nothing — the consumer sets it,
			// the run behaves identically, and nothing says so.
			ref := "inputs." + name
			if !strings.Contains(runs, ref) {
				t.Fatalf("input %q is declared but never referenced as %q anywhere in runs:. "+
					"A consumer can set it and it reaches no code. Either wire it or remove it.", name, ref)
			}

			// Layer two: if it is mapped to the conventional INPUT_ variable, some script must
			// read that variable. An input can legitimately be consumed only by an expression --
			// `comment` is used in an `if:` and `github-token` is mapped to GH_TOKEN -- so the
			// script check applies exactly when the INPUT_ mapping is present.
			env := envName(name)
			if !strings.Contains(runs, env+":") {
				return
			}

			for _, body := range scripts {
				if strings.Contains(body, env) {
					return
				}
			}

			t.Errorf("input %q is mapped to %s in action.yml, but no script under action/ reads %s. "+
				"The value is passed to code that ignores it.", name, env, env)
		})
	}
}

func TestActionOutputsAreEmitted(t *testing.T) {
	def, _, scripts := loadAction(t)

	if len(def.Outputs) == 0 {
		t.Fatal("action.yml declares no outputs; this test is not reading what it thinks it is")
	}

	for name, out := range def.Outputs {
		t.Run(name, func(t *testing.T) {
			if out.Value == "" {
				t.Fatalf("output %q declares no value:, so it is always empty for every consumer", name)
			}

			// A composite output is `${{ steps.<id>.outputs.<key> }}`. The key is what a script
			// must emit, and it is deliberately read out of the expression rather than assumed
			// equal to the output's own name: `certificate` and `gate` are documented aliases
			// that map to `certificate-path` and `gate-status`, and a test that assumed the
			// names matched would report two false failures and teach the reader to skip it.
			key := out.Value
			if i := strings.LastIndex(key, ".outputs."); i >= 0 {
				key = key[i+len(".outputs."):]
			}
			key = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(key), "}}"))

			if key == "" {
				t.Fatalf("output %q has value %q, which names no step output", name, out.Value)
			}

			// certify.sh writes outputs through `emit '<key>' ...`.
			needle := "emit '" + key + "'"
			for _, body := range scripts {
				if strings.Contains(body, needle) {
					return
				}
			}

			t.Errorf("output %q resolves to step output %q, and no script under action/ emits it (%s). "+
				"Every consumer reading this output gets an empty string.", name, key, needle)
		})
	}
}
