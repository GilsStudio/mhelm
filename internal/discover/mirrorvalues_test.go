package discover

import (
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/google/go-cmp/cmp"
)

func TestBuildMirrorValues(t *testing.T) {
	t.Run("string-form-rewrite", func(t *testing.T) {
		matches := map[string][]imageCandidate{
			"nginx:1.2": {{Path: "image", Ref: "nginx:1.2", StringForm: "nginx:1.2"}},
		}
		got := buildMirrorValues(matches, nil, nil, "oci://mirror.example.com")
		want := map[string]any{
			"image": "mirror.example.com/nginx:1.2",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("object-form-with-registry", func(t *testing.T) {
		matches := map[string][]imageCandidate{
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
		got := buildMirrorValues(matches, nil, nil, "oci://mirror.example.com")
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
		matches := map[string][]imageCandidate{
			"ghcr.io/org/app:v1": {{
				Path: "image",
				Ref:  "ghcr.io/org/app:v1",
				ObjectForm: map[string]interface{}{
					"repository": "ghcr.io/org/app",
					"tag":        "v1",
				},
			}},
		}
		got := buildMirrorValues(matches, nil, nil, "oci://mirror.example.com")
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
		matches := map[string][]imageCandidate{
			"ghcr.io/org/app@sha256:abc": {{
				Path: "image",
				Ref:  "ghcr.io/org/app@sha256:abc",
				ObjectForm: map[string]interface{}{
					"repository": "ghcr.io/org/app",
					"digest":     "sha256:abc",
				},
			}},
		}
		got := buildMirrorValues(matches, nil, nil, "oci://mirror.example.com")
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
		got := buildMirrorValues(nil, extras, merged, "oci://mirror.example.com")
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
		got := buildMirrorValues(nil, extras, merged, "oci://mirror.example.com")
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
		got := buildMirrorValues(nil, extras, nil, "oci://mirror.example.com")
		if got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})

	t.Run("empty-input-returns-nil", func(t *testing.T) {
		got := buildMirrorValues(nil, nil, nil, "oci://mirror.example.com")
		if got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})
}
