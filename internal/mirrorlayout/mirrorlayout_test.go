package mirrorlayout

import "testing"

func TestPrefixStripsSchemeAndTrailingSlash(t *testing.T) {
	cases := map[string]string{
		"oci://ghcr.io/myorg/mirror":  "ghcr.io/myorg/mirror",
		"oci://ghcr.io/myorg/mirror/": "ghcr.io/myorg/mirror",
		"ghcr.io/myorg/mirror":        "ghcr.io/myorg/mirror",
		"oci://localhost:5000/m":      "localhost:5000/m",
	}
	for in, want := range cases {
		if got := Prefix(in); got != want {
			t.Errorf("Prefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNamespaces(t *testing.T) {
	const u = "oci://ghcr.io/myorg/mirror"
	if got, want := ChartsBase(u), "ghcr.io/myorg/mirror/charts"; got != want {
		t.Errorf("ChartsBase = %q, want %q", got, want)
	}
	if got, want := PlatformBase(u), "ghcr.io/myorg/mirror/platform"; got != want {
		t.Errorf("PlatformBase = %q, want %q", got, want)
	}
	if got, want := ImagePrefix(u), "ghcr.io/myorg/mirror/images"; got != want {
		t.Errorf("ImagePrefix = %q, want %q", got, want)
	}
	if got, want := ChartRepo(u, "cilium"), "ghcr.io/myorg/mirror/charts/cilium"; got != want {
		t.Errorf("ChartRepo = %q, want %q", got, want)
	}
	if got, want := PlatformRepo(u, "cilium"), "ghcr.io/myorg/mirror/platform/cilium"; got != want {
		t.Errorf("PlatformRepo = %q, want %q", got, want)
	}
}

// The image namespace must never shadow a real upstream path: image
// paths always start with a registry host, charts/ and platform/ hold
// bare chart names. Spot-check the three are disjoint at the first segment.
func TestNamespacesAreDisjoint(t *testing.T) {
	const u = "oci://ghcr.io/myorg/mirror"
	img := ImagePrefix(u) + "/quay.io/cilium/cilium"
	chart := ChartRepo(u, "cilium")
	plat := PlatformRepo(u, "cilium")
	if img == chart || img == plat || chart == plat {
		t.Fatalf("namespaces collided: img=%q chart=%q plat=%q", img, chart, plat)
	}
}
