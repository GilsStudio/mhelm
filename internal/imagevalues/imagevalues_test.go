package imagevalues

import (
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-cmp/cmp"
)

func img(ref, digest string, paths ...string) lockfile.Image {
	i := lockfile.Image{Ref: ref, Digest: digest}
	for _, p := range paths {
		i.ValuesPaths = append(i.ValuesPaths, lockfile.ValuesPath{Path: p})
	}
	return i
}

func TestBuildTagBased(t *testing.T) {
	// downstreamURL "oci://mirror.example.com" → image namespace
	// "mirror.example.com/images" (mirrorlayout.ImagePrefix); every
	// rewrite must land under it, matching imagemirror's push.
	t.Run("string-form-rewrite", func(t *testing.T) {
		images := []lockfile.Image{img("nginx:1.2", "", "image")}
		merged := map[string]any{"image": "nginx:1.2"}
		got := BuildTagBased(images, nil, merged, "oci://mirror.example.com")
		want := map[string]any{"image": "mirror.example.com/images/nginx:1.2"}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("object-form-with-registry", func(t *testing.T) {
		images := []lockfile.Image{img("quay.io/org/app:v1", "", "controller.image")}
		merged := map[string]any{"controller": map[string]any{"image": map[string]any{
			"registry": "quay.io", "repository": "org/app", "tag": "v1",
		}}}
		got := BuildTagBased(images, nil, merged, "oci://mirror.example.com")
		want := map[string]any{"controller": map[string]any{"image": map[string]any{
			"registry": "mirror.example.com/images", "repository": "quay.io/org/app", "tag": "v1",
		}}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("object-form-without-registry", func(t *testing.T) {
		images := []lockfile.Image{img("ghcr.io/org/app:v1", "", "image")}
		merged := map[string]any{"image": map[string]any{
			"repository": "ghcr.io/org/app", "tag": "v1",
		}}
		got := BuildTagBased(images, nil, merged, "oci://mirror.example.com")
		want := map[string]any{"image": map[string]any{
			"repository": "mirror.example.com/images/ghcr.io/org/app", "tag": "v1",
		}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("object-form-preserves-chart-digest-when-none-resolved", func(t *testing.T) {
		images := []lockfile.Image{img("ghcr.io/org/app@sha256:abc", "", "image")}
		merged := map[string]any{"image": map[string]any{
			"repository": "ghcr.io/org/app", "digest": "sha256:abc",
		}}
		got := BuildTagBased(images, nil, merged, "oci://mirror.example.com")
		want := map[string]any{"image": map[string]any{
			"repository": "mirror.example.com/images/ghcr.io/org/app", "digest": "sha256:abc",
		}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("emits-resolved-digest-even-without-digest-key", func(t *testing.T) {
		// Cilium's operator.image carries genericDigest, not digest.
		images := []lockfile.Image{img("quay.io/cilium/operator-generic:v1.19.3", "sha256:res", "operator.image")}
		merged := map[string]any{"operator": map[string]any{"image": map[string]any{
			"repository": "quay.io/cilium/operator-generic", "tag": "v1.19.3", "genericDigest": "sha256:old",
		}}}
		got := BuildTagBased(images, nil, merged, "oci://mirror.example.com")
		want := map[string]any{"operator": map[string]any{"image": map[string]any{
			"repository": "mirror.example.com/images/quay.io/cilium/operator-generic",
			"tag":        "v1.19.3", "digest": "sha256:res", "genericDigest": "sha256:res",
		}}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("per-cloud-digest-suffix-match", func(t *testing.T) {
		// repository is the base (operator); rendered image is
		// operator-generic ⇒ only genericDigest is set, azureDigest left
		// to the chart's own default.
		images := []lockfile.Image{img("quay.io/cilium/operator-generic:v1.19.3", "sha256:res", "operator.image")}
		merged := map[string]any{"operator": map[string]any{"image": map[string]any{
			"repository": "quay.io/cilium/operator", "tag": "v1.19.3",
			"genericDigest": "sha256:g", "azureDigest": "sha256:a",
		}}}
		got := BuildTagBased(images, nil, merged, "oci://mirror.example.com")
		want := map[string]any{"operator": map[string]any{"image": map[string]any{
			"repository": "mirror.example.com/images/quay.io/cilium/operator", "tag": "v1.19.3",
			"digest": "sha256:res", "genericDigest": "sha256:res",
		}}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("extraImages-string-shape-inferred-from-merged", func(t *testing.T) {
		images := []lockfile.Image{img("registry.io/extra:1", "", "extraImage")}
		merged := map[string]any{"extraImage": "registry.io/extra:1"}
		got := BuildTagBased(images, nil, merged, "oci://mirror.example.com")
		want := map[string]any{"extraImage": "mirror.example.com/images/registry.io/extra:1"}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("override-path-emits-pinned-string", func(t *testing.T) {
		images := []lockfile.Image{img("quay.io/cilium/operator-generic:v1.19.3", "sha256:res", "operator.image")}
		extras := []chartfile.ExtraImage{{
			Ref: "quay.io/cilium/operator-generic:v1.19.3", ValuesPath: "operator.image", OverridePath: "operator.image.override",
		}}
		merged := map[string]any{"operator": map[string]any{"image": map[string]any{
			"repository": "quay.io/cilium/operator-generic", "tag": "v1.19.3",
		}}}
		got := BuildTagBased(images, extras, merged, "oci://mirror.example.com")
		want := map[string]any{"operator": map[string]any{"image": map[string]any{
			"repository": "mirror.example.com/images/quay.io/cilium/operator-generic",
			"tag":        "v1.19.3", "digest": "sha256:res",
			"override": "mirror.example.com/images/quay.io/cilium/operator-generic:v1.19.3@sha256:res",
		}}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("override-path-no-digest", func(t *testing.T) {
		images := []lockfile.Image{img("quay.io/cilium/op-generic:v1", "", "x.image")}
		extras := []chartfile.ExtraImage{{Ref: "quay.io/cilium/op-generic:v1", OverridePath: "x.image.override"}}
		got := BuildTagBased(images, extras, map[string]any{}, "oci://mirror.example.com")
		want := map[string]any{"x": map[string]any{"image": map[string]any{
			"override": "mirror.example.com/images/quay.io/cilium/op-generic:v1",
		}}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("no-valuesPaths-no-override-skipped", func(t *testing.T) {
		got := BuildTagBased([]lockfile.Image{img("registry.io/extra:1", "")}, nil, nil, "oci://mirror.example.com")
		if got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})

	t.Run("empty-input-returns-nil", func(t *testing.T) {
		got := BuildTagBased(nil, nil, nil, "oci://mirror.example.com")
		if got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})
}

func TestBuildDigestPinned(t *testing.T) {
	t.Run("string-form-default-pins-as-bare-ref-at-digest", func(t *testing.T) {
		images := []lockfile.Image{{
			Ref:              "nginx:1.2",
			DownstreamRef:    "mirror.example.com/nginx:1.2",
			DownstreamDigest: "sha256:aaa",
			ValuesPaths:      []lockfile.ValuesPath{{Path: "image"}},
		}}
		merged := map[string]any{"image": "nginx:1.2"}
		got := BuildDigestPinned(images, merged)
		want := map[string]any{"image": "mirror.example.com/nginx@sha256:aaa"}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("map-form-with-registry-decomposes", func(t *testing.T) {
		images := []lockfile.Image{{
			Ref:              "quay.io/org/app:v1",
			DownstreamRef:    "mirror.example.com/quay.io/org/app:v1",
			DownstreamDigest: "sha256:bbb",
			ValuesPaths:      []lockfile.ValuesPath{{Path: "ctrl.image"}},
		}}
		merged := map[string]any{
			"ctrl": map[string]any{
				"image": map[string]any{
					"registry":   "quay.io",
					"repository": "org/app",
					"tag":        "v1",
				},
			},
		}
		got := BuildDigestPinned(images, merged)
		want := map[string]any{
			"ctrl": map[string]any{
				"image": map[string]any{
					"registry":   "mirror.example.com",
					"repository": "quay.io/org/app",
					"digest":     "sha256:bbb",
				},
			},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("map-form-without-registry-uses-combined-repository", func(t *testing.T) {
		images := []lockfile.Image{{
			Ref:              "ghcr.io/org/app:v1",
			DownstreamRef:    "mirror.example.com/ghcr.io/org/app:v1",
			DownstreamDigest: "sha256:ccc",
			ValuesPaths:      []lockfile.ValuesPath{{Path: "image"}},
		}}
		merged := map[string]any{
			"image": map[string]any{
				"repository": "ghcr.io/org/app",
				"tag":        "v1",
			},
		}
		got := BuildDigestPinned(images, merged)
		want := map[string]any{
			"image": map[string]any{
				"repository": "mirror.example.com/ghcr.io/org/app",
				"digest":     "sha256:ccc",
			},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("missing-downstream-skipped", func(t *testing.T) {
		images := []lockfile.Image{{
			Ref:         "nginx:1.2",
			ValuesPaths: []lockfile.ValuesPath{{Path: "image"}},
			// No DownstreamRef/Digest set yet.
		}}
		got := BuildDigestPinned(images, nil)
		if got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})

	t.Run("no-valuesPaths-skipped", func(t *testing.T) {
		images := []lockfile.Image{{
			Ref:              "nginx:1.2",
			DownstreamRef:    "mirror.example.com/nginx:1.2",
			DownstreamDigest: "sha256:aaa",
		}}
		got := BuildDigestPinned(images, nil)
		if got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})

	t.Run("multiple-values-paths-all-rewritten", func(t *testing.T) {
		images := []lockfile.Image{{
			Ref:              "ghcr.io/org/app:v1",
			DownstreamRef:    "mirror.example.com/ghcr.io/org/app:v1",
			DownstreamDigest: "sha256:ddd",
			ValuesPaths: []lockfile.ValuesPath{
				{Path: "a.image"},
				{Path: "b.image"},
			},
		}}
		got := BuildDigestPinned(images, nil)
		want := map[string]any{
			"a": map[string]any{"image": "mirror.example.com/ghcr.io/org/app@sha256:ddd"},
			"b": map[string]any{"image": "mirror.example.com/ghcr.io/org/app@sha256:ddd"},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})
}

func TestSplitRegistryRepo(t *testing.T) {
	cases := []struct {
		in       string
		wantReg  string
		wantRepo string
	}{
		{"ghcr.io/org/app", "ghcr.io", "org/app"},
		{"ghcr.io/org/app:v1", "ghcr.io", "org/app"},
		{"ghcr.io/org/app@sha256:abc", "ghcr.io", "org/app"},
		{"localhost:5000/app", "localhost:5000", "app"},
		{"app", "", "app"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			r, p := splitRegistryRepo(tc.in)
			if r != tc.wantReg || p != tc.wantRepo {
				t.Errorf("splitRegistryRepo(%q) = (%q, %q), want (%q, %q)", tc.in, r, p, tc.wantReg, tc.wantRepo)
			}
		})
	}
}
