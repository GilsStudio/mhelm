package wrap

import (
	"strings"
	"testing"
	"time"

	"github.com/gilsstudio/mhelm/internal/lockfile"
)

func TestMissingImagesError_Format(t *testing.T) {
	err := &MissingImagesError{Missing: []string{"a/b", "c/d"}}
	got := err.Error()
	if !strings.Contains(got, "fail-safe") {
		t.Errorf("expected error to mention 'fail-safe', got: %s", got)
	}
	if !strings.Contains(got, "a/b") || !strings.Contains(got, "c/d") {
		t.Errorf("expected error to list missing images, got: %s", got)
	}
	if !strings.Contains(got, "mirror.discoveryValues") || !strings.Contains(got, "mirror.extraImages") {
		t.Errorf("expected remediation hint in error, got: %s", got)
	}
}

func TestCanonicalRepoSet(t *testing.T) {
	images := []lockfile.Image{
		{Ref: "ghcr.io/org/app:v1"},
		{Ref: "ghcr.io/org/app2@sha256:" + strings.Repeat("a", 64)},
		{Ref: "quay.io/cilium/cilium:v1.19.3"},
	}
	set := canonicalRepoSet(images)
	if !set["ghcr.io/org/app"] {
		t.Errorf("missing canonical match for ghcr.io/org/app: %v", set)
	}
	if !set["ghcr.io/org/app2"] {
		t.Errorf("missing canonical match for digest-only ref: %v", set)
	}
	if !set["quay.io/cilium/cilium"] {
		t.Errorf("missing canonical match for cilium: %v", set)
	}
}

func TestResultToLockfileBlock_RoundTrip(t *testing.T) {
	r := Result{
		ChartName:                "cilium-wrapped",
		ChartVersion:             "1.19.3-myorg.1",
		ChartContentDigest:       "sha256:aaa",
		DownstreamRef:            "ghcr.io/myorg/mirror/cilium-wrapped:1.19.3-myorg.1",
		DownstreamManifestDigest: "sha256:bbb",
		DependsOnRef:             "ghcr.io/myorg/mirror/cilium:1.19.3",
		DependsOnManifestDigest:  "sha256:ccc",
		DeployedImages:           []string{"cilium/cilium", "cilium/operator-generic"},
	}
	at := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	got := r.ToLockfileBlock("v0.3.0", at)

	if got.Chart.Name != r.ChartName {
		t.Errorf("Chart.Name = %q, want %q", got.Chart.Name, r.ChartName)
	}
	if got.Chart.OCIManifestDigest != r.DownstreamManifestDigest {
		t.Errorf("Chart.OCIManifestDigest = %q, want %q", got.Chart.OCIManifestDigest, r.DownstreamManifestDigest)
	}
	if got.DependsOn.Ref != r.DependsOnRef {
		t.Errorf("DependsOn.Ref = %q, want %q", got.DependsOn.Ref, r.DependsOnRef)
	}
	if len(got.DeployedImages) != 2 {
		t.Errorf("DeployedImages = %v", got.DeployedImages)
	}
	if got.Tool != "mhelm wrap" {
		t.Errorf("Tool = %q", got.Tool)
	}
	if got.Version != "v0.3.0" {
		t.Errorf("Version = %q", got.Version)
	}
	if !got.Timestamp.Equal(at) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, at)
	}
}
