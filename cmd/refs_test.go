package cmd

import (
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

func refsFixture() lockfile.File {
	var lf lockfile.File
	lf.Mirror.Downstream.Ref = "ghcr.io/org/mirror/cilium:1.19.3"
	lf.Mirror.Downstream.OCIManifestDigest = "sha256:chart"
	lf.Mirror.Upstream.OCIManifestDigest = "sha256:upchart"
	lf.Mirror.Images = []lockfile.Image{
		{
			Ref: "quay.io/cilium/cilium:v1.19.3", Digest: "sha256:upimg",
			DownstreamRef: "ghcr.io/org/mirror/cilium:v1.19.3", DownstreamDigest: "sha256:dnimg",
		},
		{Ref: "quay.io/cilium/operator:v1.19.3"}, // not mirrored — skipped
	}
	return lf
}

func TestCollectRefEntries_NoUpstream(t *testing.T) {
	defer resetRefsFlags()
	got := collectRefEntries(nil, refsFixture())
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (chart + 1 mirrored image)", len(got))
	}
	if got[0].Kind != "chart" || got[0].Ref != "ghcr.io/org/mirror/cilium@sha256:chart" {
		t.Errorf("chart entry = %+v", got[0])
	}
	if got[1].Kind != "image" || got[1].UpstreamRef != "" {
		t.Errorf("image entry should have no upstreamRef without cf: %+v", got[1])
	}
}

func TestCollectRefEntries_WithUpstream(t *testing.T) {
	defer resetRefsFlags()
	cf := &chartfile.File{}
	cf.Mirror.Upstream.Type = chartfile.TypeOCI
	cf.Mirror.Upstream.URL = "oci://quay.io/cilium/charts/cilium"
	cf.Mirror.Upstream.Version = "1.19.3"

	got := collectRefEntries(cf, refsFixture())
	if got[0].UpstreamRef != "quay.io/cilium/charts/cilium@sha256:upchart" {
		t.Errorf("chart upstreamRef = %q", got[0].UpstreamRef)
	}
	if got[1].UpstreamRef != "quay.io/cilium/cilium@sha256:upimg" {
		t.Errorf("image upstreamRef = %q", got[1].UpstreamRef)
	}
}

func TestCollectRefEntries_ChartOnly(t *testing.T) {
	defer resetRefsFlags()
	refsChartOnly = true
	got := collectRefEntries(nil, refsFixture())
	if len(got) != 1 || got[0].Kind != "chart" {
		t.Errorf("chart-only = %+v, want single chart entry", got)
	}
}

func resetRefsFlags() {
	refsChartOnly = false
	refsImagesOnly = false
	refsWithUpstream = false
	refsJSON = false
}
