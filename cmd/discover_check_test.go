package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/discover"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

func sampleResult() discover.Result {
	return discover.Result{
		ChartName:          "cilium",
		ChartVersion:       "1.19.3",
		ChartContentDigest: "sha256:aaa",
		Images: []lockfile.Image{
			{Ref: "quay.io/cilium/cilium:v1.19.3", Digest: "sha256:111"},
		},
	}
}

func TestBuildArtifacts_Deterministic(t *testing.T) {
	cf := chartfile.File{}
	a, _, _, _, err := buildArtifacts(sampleResult(), lockfile.File{}, cf)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, _, err := buildArtifacts(sampleResult(), lockfile.File{}, cf)
	if err != nil {
		t.Fatal(err)
	}
	an, _ := lockfile.Marshal(normForCompare(a))
	bn, _ := lockfile.Marshal(normForCompare(b))
	if !bytes.Equal(an, bn) {
		t.Errorf("normalized artifacts not deterministic:\n%s\n---\n%s", an, bn)
	}
}

func TestBuildArtifacts_StampNormalizedAwayFromDiff(t *testing.T) {
	// A prior lockfile with an OLD stamp but the SAME discovered content
	// must not be reported as stale.
	prior := lockfile.File{}
	prior.Mirror.Tool = "mhelm mirror"
	prior.Mirror.Version = "v0.3.0"
	prior.Mirror.Timestamp = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	prior.Mirror.Chart = lockfile.Chart{Name: "cilium", Version: "1.19.3"}
	prior.Mirror.Upstream.ChartContentDigest = "sha256:aaa"
	prior.Mirror.Images = []lockfile.Image{{Ref: "quay.io/cilium/cilium:v1.19.3", Digest: "sha256:111"}}

	cand, _, _, _, err := buildArtifacts(sampleResult(), prior, chartfile.File{})
	if err != nil {
		t.Fatal(err)
	}
	pn, _ := lockfile.Marshal(normForCompare(prior))
	cn, _ := lockfile.Marshal(normForCompare(cand))
	if !bytes.Equal(pn, cn) {
		t.Errorf("stamp churn leaked into diff:\n--- prior\n%s\n--- cand\n%s", pn, cn)
	}
}

func TestBuildArtifacts_ContentChangeIsDetected(t *testing.T) {
	prior := lockfile.File{}
	prior.Mirror.Chart = lockfile.Chart{Name: "cilium", Version: "1.19.3"}
	prior.Mirror.Upstream.ChartContentDigest = "sha256:aaa"
	prior.Mirror.Images = []lockfile.Image{{Ref: "quay.io/cilium/cilium:v1.19.3", Digest: "sha256:OLD"}}

	cand, _, _, _, err := buildArtifacts(sampleResult(), prior, chartfile.File{})
	if err != nil {
		t.Fatal(err)
	}
	pn, _ := lockfile.Marshal(normForCompare(prior))
	cn, _ := lockfile.Marshal(normForCompare(cand))
	if bytes.Equal(pn, cn) {
		t.Error("digest change not detected by normalized compare")
	}
	d := imageDelta(prior.Mirror.Images, cand.Mirror.Images)
	if len(d) != 1 {
		t.Fatalf("imageDelta = %v, want one changed entry", d)
	}
}
