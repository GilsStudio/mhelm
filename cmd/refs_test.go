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

// wrapFixture is refsFixture plus a mirrored wrapper chart. The
// action.yml sign loop relies on --chart-only carrying BOTH the mirrored
// chart and the wrapper (both Helm OCI artifacts syft cannot catalog) and
// --images-only carrying neither; if that partition ever breaks, syft is
// handed a chart ref again and the mirror job fails. These two tests pin
// it. See action.yml "Sign + attest every downstream artifact".
func wrapFixture() lockfile.File {
	lf := refsFixture()
	lf.Wrap = &lockfile.WrapBlock{}
	lf.Wrap.Chart.Ref = "ghcr.io/org/mirror/cilium-wrapped:1.19.3-myorg.1"
	lf.Wrap.Chart.OCIManifestDigest = "sha256:wrap"
	return lf
}

func TestCollectRefEntries_ChartOnly_IncludesWrapper(t *testing.T) {
	defer resetRefsFlags()
	refsChartOnly = true
	got := collectRefEntries(nil, wrapFixture())
	if len(got) != 2 {
		t.Fatalf("chart-only with wrap = %d entries, want 2 (mirrored chart + wrapper)", len(got))
	}
	for i, e := range got {
		if e.Kind != "chart" {
			t.Errorf("entry %d kind = %q, want chart (%+v)", i, e.Kind, e)
		}
	}
	if got[0].Ref != "ghcr.io/org/mirror/cilium@sha256:chart" ||
		got[1].Ref != "ghcr.io/org/mirror/cilium-wrapped@sha256:wrap" {
		t.Errorf("chart-only refs = %q, %q", got[0].Ref, got[1].Ref)
	}
}

func TestCollectRefEntries_ImagesOnly_ExcludesChartAndWrapper(t *testing.T) {
	defer resetRefsFlags()
	refsImagesOnly = true
	got := collectRefEntries(nil, wrapFixture())
	if len(got) != 1 || got[0].Kind != "image" {
		t.Fatalf("images-only = %+v, want single image entry (no chart/wrapper)", got)
	}
	if got[0].Ref != "ghcr.io/org/mirror/cilium@sha256:dnimg" {
		t.Errorf("image ref = %q", got[0].Ref)
	}
}

func resetRefsFlags() {
	refsChartOnly = false
	refsImagesOnly = false
	refsWithUpstream = false
	refsJSON = false
}
