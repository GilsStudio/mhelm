package imagevalues

import (
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-cmp/cmp"
)

func TestBuildTagBased(t *testing.T) {
	t.Run("string-form-rewrite", func(t *testing.T) {
		matches := map[string][]Candidate{
			"nginx:1.2": {{Path: "image", Ref: "nginx:1.2", StringForm: "nginx:1.2"}},
		}
		got := BuildTagBased(matches, nil, nil, "oci://mirror.example.com")
		want := map[string]any{
			"image": "mirror.example.com/nginx:1.2",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("object-form-with-registry", func(t *testing.T) {
		matches := map[string][]Candidate{
			"quay.io/org/app:v1": {{
				Path: "controller.image",
				Ref:  "quay.io/org/app:v1",
				ObjectForm: map[string]interface{}{
					"registry":   "quay.io",
					"repository": "org/app",
					"tag":        "v1",
				},
			}},
		}
		got := BuildTagBased(matches, nil, nil, "oci://mirror.example.com")
		want := map[string]any{
			"controller": map[string]any{
				"image": map[string]any{
					"registry":   "mirror.example.com",
					"repository": "quay.io/org/app",
					"tag":        "v1",
				},
			},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("object-form-without-registry", func(t *testing.T) {
		matches := map[string][]Candidate{
			"ghcr.io/org/app:v1": {{
				Path: "image",
				Ref:  "ghcr.io/org/app:v1",
				ObjectForm: map[string]interface{}{
					"repository": "ghcr.io/org/app",
					"tag":        "v1",
				},
			}},
		}
		got := BuildTagBased(matches, nil, nil, "oci://mirror.example.com")
		want := map[string]any{
			"image": map[string]any{
				"repository": "mirror.example.com/ghcr.io/org/app",
				"tag":        "v1",
			},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("object-form-preserves-digest", func(t *testing.T) {
		matches := map[string][]Candidate{
			"ghcr.io/org/app@sha256:abc": {{
				Path: "image",
				Ref:  "ghcr.io/org/app@sha256:abc",
				ObjectForm: map[string]interface{}{
					"repository": "ghcr.io/org/app",
					"digest":     "sha256:abc",
				},
			}},
		}
		got := BuildTagBased(matches, nil, nil, "oci://mirror.example.com")
		want := map[string]any{
			"image": map[string]any{
				"repository": "mirror.example.com/ghcr.io/org/app",
				"digest":     "sha256:abc",
			},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("extraImages-string-shape-inferred-from-merged", func(t *testing.T) {
		extras := []chartfile.ExtraImage{
			{Ref: "registry.io/extra:1", ValuesPath: "extraImage"},
		}
		merged := map[string]any{"extraImage": "registry.io/extra:1"}
		got := BuildTagBased(nil, extras, merged, "oci://mirror.example.com")
		want := map[string]any{
			"extraImage": "mirror.example.com/registry.io/extra:1",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("extraImages-map-shape-inferred-from-merged", func(t *testing.T) {
		extras := []chartfile.ExtraImage{
			{Ref: "quay.io/org/extra:v1", ValuesPath: "ctrl.image"},
		}
		merged := map[string]any{
			"ctrl": map[string]any{
				"image": map[string]any{
					"registry":   "quay.io",
					"repository": "org/extra",
					"tag":        "v1",
				},
			},
		}
		got := BuildTagBased(nil, extras, merged, "oci://mirror.example.com")
		want := map[string]any{
			"ctrl": map[string]any{
				"image": map[string]any{
					"registry":   "mirror.example.com",
					"repository": "quay.io/org/extra",
					"tag":        "v1",
				},
			},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("extraImages-no-valuesPath-skipped", func(t *testing.T) {
		extras := []chartfile.ExtraImage{{Ref: "registry.io/extra:1"}}
		got := BuildTagBased(nil, extras, nil, "oci://mirror.example.com")
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
