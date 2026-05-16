package wrapfp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

const schema = "mhelm.io/v1alpha1"

// fixture writes helm/values.yml + helm/extra.yml in a fresh dir and
// returns the dir plus a chartfile.File wired to reference them.
func fixture(t *testing.T) (string, chartfile.File) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "helm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helm/values.yml"), []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helm/extra.yml"), []byte("kind: ConfigMap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cf := chartfile.File{
		APIVersion: schema,
		Mirror: chartfile.Mirror{
			Upstream:        chartfile.Endpoint{Type: "oci", URL: "oci://quay.io/cilium/charts/cilium", Version: "1.19.3"},
			Downstream:      chartfile.Endpoint{Type: "oci", URL: "oci://ghcr.io/myorg/mirror"},
			DiscoveryValues: []string{"helm/values.yml"},
		},
		Wrap: &chartfile.Wrap{
			Version:        "1.19.3-myorg.1",
			ValuesFiles:    []string{"helm/values.yml"},
			ExtraManifests: []string{"helm/extra.yml"},
		},
	}
	return dir, cf
}

func mustCompute(t *testing.T, cf chartfile.File, dir, up string) string {
	t.Helper()
	fp, err := Compute(cf, dir, up, schema)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	return fp
}

func TestCompute_DeterministicAndSensitive(t *testing.T) {
	dir, cf := fixture(t)
	base := mustCompute(t, cf, dir, "sha256:up")

	// Deterministic across calls.
	if again := mustCompute(t, cf, dir, "sha256:up"); again != base {
		t.Fatalf("non-deterministic: %s != %s", base, again)
	}

	// One byte changed in any referenced input file moves the digest.
	// discoveryValues and wrap.valuesFiles both point at helm/values.yml
	// in the fixture, so this also covers the wrap.valuesFiles input.
	fileCases := []struct {
		name, path, body string
	}{
		{"values.yml (discoveryValues + wrap.valuesFiles)", "helm/values.yml", "a: 2\n"},
		{"extra.yml (wrap.extraManifests)", "helm/extra.yml", "kind: Secret\n"},
	}
	for _, tc := range fileCases {
		t.Run(tc.name, func(t *testing.T) {
			d, c := fixture(t)
			before := mustCompute(t, c, d, "sha256:up")
			if err := os.WriteFile(filepath.Join(d, tc.path), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if after := mustCompute(t, c, d, "sha256:up"); after == before {
				t.Errorf("%s: digest unchanged after a one-byte input mutation", tc.name)
			}
		})
	}

	// chart.json change (a wrap.version bump is a chart.json change).
	cf2 := cf
	w := *cf.Wrap
	w.Version = "1.19.3-myorg.2"
	cf2.Wrap = &w
	if got := mustCompute(t, cf2, dir, "sha256:up"); got == base {
		t.Error("chart.json change did not move the fingerprint")
	}

	// Upstream chart digest change.
	if got := mustCompute(t, cf, dir, "sha256:DIFFERENT"); got == base {
		t.Error("upstream chart digest change did not move the fingerprint")
	}

	// Schema version change.
	if got, _ := Compute(cf, dir, "sha256:up", "mhelm.io/v2"); got == base {
		t.Error("schema version change did not move the fingerprint")
	}
}

func TestCompute_MissingFileErrors(t *testing.T) {
	dir, cf := fixture(t)
	if err := os.Remove(filepath.Join(dir, "helm/values.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Compute(cf, dir, "sha256:up", schema); err == nil {
		t.Fatal("expected an error fingerprinting over a missing input file")
	}
}

func TestCompute_NoWrapSection(t *testing.T) {
	dir, cf := fixture(t)
	cf.Wrap = nil
	if _, err := Compute(cf, dir, "sha256:up", schema); err != nil {
		t.Fatalf("Compute without wrap section should still work (discoveryValues only): %v", err)
	}
}

func TestClassifyPrior(t *testing.T) {
	mk := func(version, digest string) *lockfile.WrapBlock {
		return &lockfile.WrapBlock{
			Chart:        lockfile.WrapChart{Version: version},
			InputsDigest: digest,
		}
	}
	cases := []struct {
		name        string
		prior       *lockfile.WrapBlock
		wrapVersion string
		fp          string
		want        PriorState
	}{
		{"no prior", nil, "1.0", "sha256:a", FirstWrap},
		{"pre-v0.7 (no digest)", mk("1.0", ""), "1.0", "sha256:a", PreV07},
		{"idempotent re-wrap", mk("1.0", "sha256:a"), "1.0", "sha256:a", InSync},
		{"version bumped", mk("1.0", "sha256:a"), "1.1", "sha256:b", VersionBumped},
		{"version reuse w/ changed inputs", mk("1.0", "sha256:a"), "1.0", "sha256:b", VersionReuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPrior(tc.prior, tc.wrapVersion, tc.fp); got != tc.want {
				t.Errorf("ClassifyPrior = %v, want %v", got, tc.want)
			}
		})
	}
}
