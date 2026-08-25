// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	snap "github.com/VIKOIT/reversibility-engine/internal/snapshot"
)

// Kubernetes collects metadata from a cluster.
//
// Only GET and LIST are ever issued. There is no create, update, patch, or delete anywhere in
// this file, and there is no code path that could add one without it being visible in review —
// which is what makes "read-only" a property of the program rather than a promise about it.
type Kubernetes struct {
	// Context is the kubeconfig context to use. Empty means the current one.
	Context string

	// Kubeconfig is an explicit path. Empty means the usual resolution: $KUBECONFIG, then
	// ~/.kube/config, then in-cluster credentials.
	Kubeconfig string

	// Environment labels the source in the resulting file.
	Environment string

	// Now is the clock, injected so a collector run is reproducible in tests.
	Now func() time.Time
}

// Collect reads storage and workload metadata from the cluster.
//
// Nothing about a pod's environment, no ConfigMap contents, and no Secret — not even a Secret's
// name — is read. The three things collected are the three the rules can use, and the list in
// docs/PRODUCTION-CONTEXT.md is exhaustive.
func (k Kubernetes) Collect(ctx context.Context) (*snap.Snapshot, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if k.Kubeconfig != "" {
		rules.ExplicitPath = k.Kubeconfig
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{CurrentContext: k.Context},
	)

	cfg, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("resolving cluster credentials: %w", err)
	}
	cfg.UserAgent = "reversibility-engine-snapshot"

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building a cluster client: %w", err)
	}

	now := time.Now
	if k.Now != nil {
		now = k.Now
	}

	data := &snap.KubernetesData{}

	if data.StorageClasses, err = collectStorageClasses(ctx, client); err != nil {
		return nil, err
	}
	if data.Claims, err = collectClaims(ctx, client); err != nil {
		return nil, err
	}
	if data.Workloads, err = collectWorkloads(ctx, client); err != nil {
		return nil, err
	}

	fingerprint, err := k.fingerprint(clientConfig, cfg.Host)
	if err != nil {
		return nil, err
	}

	out := &snap.Snapshot{
		SchemaVersion:     snap.SchemaVersion,
		Kind:              snap.KindKubernetes,
		Environment:       k.Environment,
		CollectedAt:       now().UTC().Truncate(time.Second),
		SourceFingerprint: fingerprint,
		Kubernetes:        data,
	}
	out.Canonicalize()
	return out, nil
}

// fingerprint identifies the cluster without recording how to reach it.
//
// The API server URL is hashed rather than stored for the same reason a DSN is never stored: it
// is an access hint, and a snapshot file travels further than the credentials that produced it.
func (k Kubernetes) fingerprint(clientConfig clientcmd.ClientConfig, host string) (string, error) {
	raw, err := clientConfig.RawConfig()
	if err != nil {
		return "", fmt.Errorf("reading the kubeconfig: %w", err)
	}

	contextName := k.Context
	if contextName == "" {
		contextName = raw.CurrentContext
	}

	clusterName := contextName
	if c, ok := raw.Contexts[contextName]; ok && c != nil {
		clusterName = c.Cluster
	}

	sum := sha256.Sum256([]byte("kubernetes\x00" + clusterName + "\x00" + host))
	return hex.EncodeToString(sum[:]), nil
}

func collectStorageClasses(ctx context.Context, client kubernetes.Interface) ([]snap.StorageClass, error) {
	list, err := client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing storage classes: %w", err)
	}

	out := make([]snap.StorageClass, 0, len(list.Items))
	for _, sc := range list.Items {
		policy := ""
		if sc.ReclaimPolicy != nil {
			policy = string(*sc.ReclaimPolicy)
		}
		out = append(out, snap.StorageClass{Name: sc.Name, ReclaimPolicy: policy})
	}
	return out, nil
}

func collectClaims(ctx context.Context, client kubernetes.Interface) ([]snap.Claim, error) {
	list, err := client.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing persistent volume claims: %w", err)
	}

	out := make([]snap.Claim, 0, len(list.Items))
	for _, pvc := range list.Items {
		claim := snap.Claim{
			Namespace: pvc.Namespace,
			Name:      pvc.Name,
			Phase:     string(pvc.Status.Phase),
		}
		if pvc.Spec.StorageClassName != nil {
			claim.StorageClass = *pvc.Spec.StorageClassName
		}
		// The bound capacity, not the request. Only the bound size constrains a shrink, and the
		// two differ whenever a provisioner rounded the request up.
		if q, ok := pvc.Status.Capacity["storage"]; ok {
			claim.Capacity = q.String()
		}
		out = append(out, claim)
	}
	return out, nil
}

func collectWorkloads(ctx context.Context, client kubernetes.Interface) ([]snap.Workload, error) {
	var out []snap.Workload

	deployments, err := client.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}
	for _, d := range deployments.Items {
		replicas := int32(0)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		out = append(out, snap.Workload{Namespace: d.Namespace, Kind: "Deployment", Name: d.Name, Replicas: replicas})
	}

	statefulSets, err := client.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing stateful sets: %w", err)
	}
	for _, s := range statefulSets.Items {
		replicas := int32(0)
		if s.Spec.Replicas != nil {
			replicas = *s.Spec.Replicas
		}
		out = append(out, snap.Workload{Namespace: s.Namespace, Kind: "StatefulSet", Name: s.Name, Replicas: replicas})
	}

	daemonSets, err := client.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing daemon sets: %w", err)
	}
	for _, d := range daemonSets.Items {
		// A DaemonSet has no replica count; the scheduled count is the closest equivalent and
		// is what an operator compares against after a rollback.
		out = append(out, snap.Workload{
			Namespace: d.Namespace, Kind: "DaemonSet", Name: d.Name,
			Replicas: d.Status.DesiredNumberScheduled,
		})
	}

	return out, nil
}
