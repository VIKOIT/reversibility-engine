package kubernetes_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// modified builds a ChangedFile from two manifest strings.
func modified(path, old, updated string) domain.ChangedFile {
	return domain.ChangedFile{
		Path:     path,
		Status:   domain.StatusModified,
		Previous: []byte(old),
		Current:  []byte(updated),
	}
}

func removed(path, old string) domain.ChangedFile {
	return domain.ChangedFile{Path: path, Status: domain.StatusRemoved, Previous: []byte(old)}
}

func analyze(t *testing.T, files ...domain.ChangedFile) []domain.Finding {
	t.Helper()

	got, err := kubernetes.New().Analyze(context.Background(), files)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return got
}

func ruleIDs(findings []domain.Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.RuleID)
	}
	return out
}

const pinnedImage = "ghcr.io/acme/api@sha256:3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea"

func deployment(replicas int, image string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: web
spec:
  replicas: %d
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
        - name: api
          image: %s
`, replicas, image)
}

func pvc(size string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: uploads
  namespace: web
spec:
  storageClassName: gp3
  resources:
    requests:
      storage: %s
`, size)
}

// A multi-document manifest must be diffed object by object. sigs.k8s.io/yaml decodes only the
// first document and reports no error, so without explicit splitting every object after the
// first would vanish — taking any destructive change with it.
func TestAnalyzeMultiDocumentManifest(t *testing.T) {
	t.Parallel()

	const old = `apiVersion: v1
kind: Namespace
metadata:
  name: legacy
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
  namespace: web
data:
  a: "1"
---
apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: web
spec:
  type: ClusterIP
`

	// The Namespace is dropped and the Service type changes. Both live at or after the first
	// separator, which is exactly what a truncating parser would miss.
	const updated = `apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
  namespace: web
data:
  a: "1"
---
apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: web
spec:
  type: LoadBalancer
`

	got := analyze(t, modified("bundle.yaml", old, updated))

	want := map[string]bool{"K8S006": false, "K8S007": false}
	for _, f := range got {
		if _, expected := want[f.RuleID]; expected {
			want[f.RuleID] = true
		}
	}
	for rule, found := range want {
		if !found {
			t.Errorf("%s was not reported; findings were %v", rule, ruleIDs(got))
		}
	}
}

// Context objects answer questions about the change; they are not the change. Reporting them
// would mean every run indicted the entire cluster.
func TestUnchangedObjectsProduceNoFindings(t *testing.T) {
	t.Parallel()

	manifest := deployment(3, pinnedImage)

	if got := analyze(t, modified("deployment.yaml", manifest, manifest)); len(got) != 0 {
		t.Errorf("an unchanged manifest produced %d findings: %v", len(got), ruleIDs(got))
	}
}

// Only an explicit Retain is safe. CLAUDE.md §10 treats an unknown reclaim policy as Delete.
func TestPVCRemovedWithRetainPolicy(t *testing.T) {
	t.Parallel()

	const storageClass = `apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: gp3
reclaimPolicy: Retain
`

	got := analyze(t,
		modified("sc.yaml", storageClass, storageClass),
		removed("pvc.yaml", pvc("20Gi")),
	)

	for _, f := range got {
		if f.RuleID == "K8S003" {
			t.Errorf("K8S003 fired for a Retain StorageClass: %s", f.Rationale)
		}
	}
}

func TestPVCRemovedWithUnresolvableStorageClass(t *testing.T) {
	t.Parallel()

	got := analyze(t, removed("pvc.yaml", pvc("20Gi")))

	var found bool
	for _, f := range got {
		if f.RuleID == "K8S003" && f.Reversibility == domain.ReversibilityIrreversible {
			found = true
		}
	}
	if !found {
		t.Errorf("an unresolvable StorageClass did not fail closed; findings were %v", ruleIDs(got))
	}
}

// K8S009 is about a dangling reference. With nothing referencing it, deleting a ConfigMap is a
// different problem and must not be reported as this one.
func TestConfigMapRemovedWithNoReferent(t *testing.T) {
	t.Parallel()

	const cfg = `apiVersion: v1
kind: ConfigMap
metadata:
  name: orphan
  namespace: web
data:
  a: "1"
`

	for _, f := range analyze(t, removed("cm.yaml", cfg)) {
		if f.RuleID == "K8S009" {
			t.Errorf("K8S009 fired for a ConfigMap nothing references")
		}
	}
}

// A Secret mounted as a volume is referenced just as surely as one injected through envFrom.
func TestSecretReferencedByVolume(t *testing.T) {
	t.Parallel()

	const secret = `apiVersion: v1
kind: Secret
metadata:
  name: api-tls
  namespace: web
`
	workload := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: web
spec:
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      volumes:
        - name: tls
          secret:
            secretName: api-tls
      containers:
        - name: api
          image: ` + pinnedImage + `
`

	got := analyze(t, modified("deploy.yaml", workload, workload), removed("secret.yaml", secret))

	var found bool
	for _, f := range got {
		if f.RuleID == "K8S009" {
			found = true
		}
	}
	if !found {
		t.Errorf("a Secret mounted as a volume was not detected as referenced; findings were %v", ruleIDs(got))
	}
}

// A changed object that matches no rule must not pass silently. This is the Kubernetes analogue
// of PG027 and the reason an unmatched change cannot reach grade A.
//
// A Service port change is the example: K8S007 covers only spec.type and spec.clusterIP, so
// nothing in the table describes this and the engine refuses to vouch for it.
func TestChangedObjectMatchingNoRuleIsUnknown(t *testing.T) {
	t.Parallel()

	service := func(port int) string {
		return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: web
spec:
  type: ClusterIP
  ports:
    - port: %d
      targetPort: 8080
`, port)
	}

	got := analyze(t, modified("service.yaml", service(80), service(8080)))

	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(got), ruleIDs(got))
	}
	if got[0].RuleID != "K8S014" || got[0].Reversibility != domain.ReversibilityUnknown {
		t.Errorf("got %s/%s, want K8S014/UNKNOWN", got[0].RuleID, got[0].Reversibility)
	}
}

// K8S015: the ordinary deploy. A digest is content-addressed, so the previous digest names the
// exact bytes to roll back to — the one image reference static analysis can prove is immutable.
func TestDigestPinnedImageBumpIsReversible(t *testing.T) {
	t.Parallel()

	const otherDigest = "ghcr.io/acme/api@sha256:b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c"

	got := analyze(t, modified("deployment.yaml", deployment(3, pinnedImage), deployment(3, otherDigest)))

	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(got), ruleIDs(got))
	}
	if got[0].RuleID != "K8S015" || got[0].Reversibility != domain.ReversibilityReversible {
		t.Fatalf("got %s/%s, want K8S015/REVERSIBLE", got[0].RuleID, got[0].Reversibility)
	}

	// The undo step must name the digest to return to, or it is not an undo step.
	if !strings.Contains(string(got[0].UndoStep), pinnedImage) {
		t.Errorf("undo step %q does not name the previous digest", got[0].UndoStep)
	}
}

// A tag is a mutable pointer however it is spelled, so moving to one is never K8S015. Semver is
// the case most likely to be mistaken for immutable, which is exactly why it is tested.
func TestSemverTagIsStillCostly(t *testing.T) {
	t.Parallel()

	got := analyze(t, modified("deployment.yaml", deployment(3, pinnedImage), deployment(3, "ghcr.io/acme/api:v2.1.0")))

	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(got), ruleIDs(got))
	}
	if got[0].RuleID != "K8S008" || got[0].Reversibility != domain.ReversibilityCostly {
		t.Errorf("got %s/%s, want K8S008/COSTLY — a semver tag is not a digest", got[0].RuleID, got[0].Reversibility)
	}
}

// Identical input must produce an identical result. Object identity lives in a map, and map
// iteration order would otherwise leak into the certificate.
func TestAnalyzeIsDeterministic(t *testing.T) {
	t.Parallel()

	const old = `apiVersion: v1
kind: Namespace
metadata:
  name: a
---
apiVersion: v1
kind: Namespace
metadata:
  name: b
---
apiVersion: v1
kind: Namespace
metadata:
  name: c
---
apiVersion: v1
kind: Namespace
metadata:
  name: d
`

	files := []domain.ChangedFile{removed("ns.yaml", old)}

	first := analyze(t, files...)
	if len(first) != 4 {
		t.Fatalf("got %d findings, want 4", len(first))
	}

	for i := 0; i < 50; i++ {
		if diff := cmp.Diff(first, analyze(t, files...)); diff != "" {
			t.Fatalf("run %d differed (-first +got):\n%s", i, diff)
		}
	}
}

// Every Kubernetes finding carries LockHazard NONE, per the owner's ruling in CLAUDE.md §15.2.
// The fixtures assert it for the paths they cover; this covers the synthesized ones.
func TestSynthesizedFindingsCarryNoLockHazard(t *testing.T) {
	t.Parallel()

	got := analyze(t,
		domain.ChangedFile{Path: "broken.yaml", Status: domain.StatusAdded, Current: []byte("spec:\n replicas: [3\n")},
		modified("deployment.yaml", deployment(3, pinnedImage), deployment(3, "ghcr.io/acme/api:latest")),
	)

	if len(got) == 0 {
		t.Fatal("expected findings")
	}
	for _, f := range got {
		if f.LockHazard != domain.LockNone {
			t.Errorf("%s at %s has LockHazard %q, want NONE", f.RuleID, f.File, f.LockHazard)
		}
		if f.Reversibility == domain.ReversibilityUnknown && f.UndoStep != "" {
			t.Errorf("%s is UNKNOWN yet offers an undo step %q", f.RuleID, f.UndoStep)
		}
	}
}

// An added object of an unrecognized kind is UNKNOWN, not a new workload. A custom resource can
// own arbitrary infrastructure, so K8S013's "adding takes nothing away" does not apply to it.
func TestUnrecognizedKindIsNotTreatedAsNewWorkload(t *testing.T) {
	t.Parallel()

	got := analyze(t, domain.ChangedFile{
		Path:   "widget.yaml",
		Status: domain.StatusAdded,
		Current: []byte(`apiVersion: acme.io/v1
kind: FlurbleWidget
metadata:
  name: flurble
  namespace: web
`),
	})

	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(got), ruleIDs(got))
	}
	if got[0].RuleID != "K8S014" {
		t.Errorf("RuleID = %q, want K8S014", got[0].RuleID)
	}
}

// A storage request that cannot be read must be UNKNOWN rather than assumed unchanged.
func TestUnreadableStorageQuantityIsUnknown(t *testing.T) {
	t.Parallel()

	got := analyze(t, modified("pvc.yaml", pvc("20Gi"), pvc("lots")))

	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(got), ruleIDs(got))
	}
	if got[0].RuleID != "K8S014" || got[0].Reversibility != domain.ReversibilityUnknown {
		t.Errorf("got %s/%s, want K8S014/UNKNOWN", got[0].RuleID, got[0].Reversibility)
	}
}

// A growing volume is an ordinary, supported operation and must not be reported as a shrink.
func TestPVCStorageIncreaseIsNotFlagged(t *testing.T) {
	t.Parallel()

	for _, f := range analyze(t, modified("pvc.yaml", pvc("10Gi"), pvc("20Gi"))) {
		if f.RuleID == "K8S004" {
			t.Errorf("K8S004 fired for a volume that grew")
		}
	}
}

// 1Gi is larger than 1G. A comparison that ignored the scale would call this shrink a growth.
func TestPVCStorageScaleChangeIsDetected(t *testing.T) {
	t.Parallel()

	var found bool
	for _, f := range analyze(t, modified("pvc.yaml", pvc("1Gi"), pvc("1G"))) {
		if f.RuleID == "K8S004" {
			found = true
		}
	}
	if !found {
		t.Error("a shrink from 1Gi to 1G was not detected")
	}
}

func TestAnalyzeEmptyChangeset(t *testing.T) {
	t.Parallel()

	got := analyze(t,
		domain.ChangedFile{Path: "README.md", Status: domain.StatusModified, Current: []byte("# hi")},
		domain.ChangedFile{Path: "migrations/0001.up.sql", Status: domain.StatusAdded, Current: []byte("DROP TABLE t;")},
	)
	if len(got) != 0 {
		t.Errorf("got %d findings for a changeset with no manifests, want 0: %v", len(got), ruleIDs(got))
	}
}
