package release

import (
	"strings"
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

func baseChartfile() chartfile.File {
	return chartfile.File{
		APIVersion: chartfile.APIVersion,
		Mirror: chartfile.Mirror{
			Upstream: chartfile.Endpoint{
				Type: chartfile.TypeRepo, Name: "cilium",
				URL: "https://example.com/charts", Version: "1.19.3",
			},
			Downstream: chartfile.Endpoint{
				Type: chartfile.TypeOCI,
				URL:  "oci://ghcr.io/myorg/mirror",
			},
		},
		Release: &chartfile.Release{
			Name:      "cilium",
			Namespace: "kube-system",
		},
	}
}

func lockWithMirror() lockfile.File {
	return lockfile.File{
		APIVersion: lockfile.APIVersion,
		Mirror: lockfile.MirrorBlock{
			Chart: lockfile.Chart{Name: "cilium", Version: "1.19.3"},
			Downstream: lockfile.Downstream{
				Ref:               "ghcr.io/myorg/mirror/cilium:1.19.3",
				OCIManifestDigest: "sha256:mmm",
			},
		},
	}
}

func lockWithWrap() lockfile.File {
	lf := lockWithMirror()
	lf.Wrap = &lockfile.WrapBlock{
		Chart: lockfile.WrapChart{
			Name:              "cilium-wrapped",
			Version:           "1.19.3-myorg.1",
			Ref:               "ghcr.io/myorg/mirror/cilium-wrapped:1.19.3-myorg.1",
			OCIManifestDigest: "sha256:www",
		},
	}
	return lf
}

func TestResolve_PicksWrapWhenPresent(t *testing.T) {
	p, err := Resolve(baseChartfile(), lockWithWrap())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Source != SourceWrap {
		t.Errorf("Source = %q, want %q", p.Source, SourceWrap)
	}
	if p.OCIRef != "ghcr.io/myorg/mirror/cilium-wrapped" {
		t.Errorf("OCIRef = %q", p.OCIRef)
	}
	if p.Version != "1.19.3-myorg.1" {
		t.Errorf("Version = %q", p.Version)
	}
	if p.ManifestDigest != "sha256:www" {
		t.Errorf("ManifestDigest = %q", p.ManifestDigest)
	}
}

func TestResolve_FallsBackToMirror(t *testing.T) {
	p, err := Resolve(baseChartfile(), lockWithMirror())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Source != SourceMirror {
		t.Errorf("Source = %q, want %q", p.Source, SourceMirror)
	}
	if p.OCIRef != "ghcr.io/myorg/mirror/cilium" {
		t.Errorf("OCIRef = %q", p.OCIRef)
	}
	if p.Version != "1.19.3" {
		t.Errorf("Version = %q", p.Version)
	}
	if p.ManifestDigest != "sha256:mmm" {
		t.Errorf("ManifestDigest = %q", p.ManifestDigest)
	}
}

func TestResolve_NoReleaseErrors(t *testing.T) {
	cf := baseChartfile()
	cf.Release = nil
	_, err := Resolve(cf, lockWithWrap())
	if err != ErrNoRelease {
		t.Errorf("Resolve = %v, want ErrNoRelease", err)
	}
}

func TestResolve_NoArtifactErrors(t *testing.T) {
	cf := baseChartfile()
	lf := lockfile.File{APIVersion: lockfile.APIVersion}
	_, err := Resolve(cf, lf)
	if err != ErrNoArtifact {
		t.Errorf("Resolve = %v, want ErrNoArtifact", err)
	}
}

func TestPlanRender_Wrap(t *testing.T) {
	p := Plan{
		Source:         SourceWrap,
		ReleaseName:    "cilium",
		Namespace:      "kube-system",
		OCIRef:         "ghcr.io/myorg/mirror/cilium-wrapped",
		Version:        "1.19.3-myorg.1",
		ManifestDigest: "sha256:www",
		ValuesFiles:    []string{"helm/install-overrides.yml", "helm/dev.yml"},
	}
	got := p.Render()

	// Comments carry source + chart + digest for audit.
	for _, want := range []string{
		"# source:  wrap",
		"# chart:   ghcr.io/myorg/mirror/cilium-wrapped:1.19.3-myorg.1",
		"# digest:  sha256:www",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing line %q in output:\n%s", want, got)
		}
	}

	// Command body.
	for _, want := range []string{
		"helm upgrade --install cilium oci://ghcr.io/myorg/mirror/cilium-wrapped",
		"--version 1.19.3-myorg.1",
		"--namespace kube-system",
		"--create-namespace",
		"-f helm/install-overrides.yml",
		"-f helm/dev.yml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing fragment %q in output:\n%s", want, got)
		}
	}

	// Backslash continuations join all lines but the last into one bash statement.
	if !strings.Contains(got, "\\\n  ") {
		t.Errorf("expected backslash continuations:\n%s", got)
	}

	// No trailing backslash on the last line (would break bash parsing).
	if strings.HasSuffix(strings.TrimRight(got, "\n"), `\`) {
		t.Errorf("trailing backslash on last line:\n%s", got)
	}
}

func TestPlanRender_NoValuesFilesOmitsFFlags(t *testing.T) {
	p := Plan{
		Source:      SourceMirror,
		ReleaseName: "cilium",
		Namespace:   "kube-system",
		OCIRef:      "ghcr.io/myorg/mirror/cilium",
		Version:     "1.19.3",
	}
	got := p.Render()
	if strings.Contains(got, "-f ") {
		t.Errorf("unexpected -f line in output for empty ValuesFiles:\n%s", got)
	}
}

func TestPlanRender_NoDigestOmitsLine(t *testing.T) {
	p := Plan{
		Source:      SourceMirror,
		ReleaseName: "cilium",
		Namespace:   "kube-system",
		OCIRef:      "ghcr.io/myorg/mirror/cilium",
		Version:     "1.19.3",
	}
	got := p.Render()
	if strings.Contains(got, "# digest:") {
		t.Errorf("unexpected digest line when no digest set:\n%s", got)
	}
}

func TestStripTag(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ghcr.io/myorg/mirror/cilium:1.19.3", "ghcr.io/myorg/mirror/cilium"},
		{"ghcr.io/myorg/mirror/cilium@sha256:abc", "ghcr.io/myorg/mirror/cilium"},
		{"ghcr.io/myorg/mirror/cilium", "ghcr.io/myorg/mirror/cilium"},
		{"localhost:5000/cilium:1.19.3", "localhost:5000/cilium"},
		{"localhost:5000/cilium", "localhost:5000/cilium"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := stripTag(tc.in); got != tc.want {
				t.Errorf("stripTag(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
