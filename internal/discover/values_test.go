package discover

import (
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestFindImageCandidates(t *testing.T) {
	values := map[string]interface{}{
		"image": "nginx:1.25",
		"sub": map[string]interface{}{
			"image": map[string]interface{}{
				"registry":   "quay.io",
				"repository": "org/app",
				"tag":        "v1",
			},
		},
		"controller": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "ghcr.io/org/ctrl",
				"digest":     "sha256:abcd",
			},
			"sidecar": map[string]interface{}{
				"image": "ghcr.io/org/side:0.1",
			},
		},
		"empty": map[string]interface{}{
			"image": "",
		},
		"nested-image-block-inside-image": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "ghcr.io/org/outer",
				"tag":        "1",
				"sub": map[string]interface{}{
					"image": "ghcr.io/org/inner:2",
				},
			},
		},
	}

	got := findImageCandidates(values)
	gotPaths := pathsOf(got)
	sort.Strings(gotPaths)
	want := []string{
		"controller.image",
		"controller.sidecar.image",
		"image",
		"nested-image-block-inside-image.image",
		"nested-image-block-inside-image.image.sub.image",
		"sub.image",
	}
	if diff := cmp.Diff(want, gotPaths); diff != "" {
		t.Errorf("paths mismatch (-want +got):\n%s", diff)
	}

	// Check refs for the two image forms at the top-level paths.
	refByPath := map[string]string{}
	for _, c := range got {
		refByPath[c.Path] = c.Ref
	}
	cases := map[string]string{
		"image":                                         "nginx:1.25",
		"sub.image":                                     "quay.io/org/app:v1",
		"controller.image":                              "ghcr.io/org/ctrl@sha256:abcd",
		"controller.sidecar.image":                      "ghcr.io/org/side:0.1",
		"nested-image-block-inside-image.image":         "ghcr.io/org/outer:1",
		"nested-image-block-inside-image.image.sub.image": "ghcr.io/org/inner:2",
	}
	for path, wantRef := range cases {
		if got := refByPath[path]; got != wantRef {
			t.Errorf("ref at %q = %q, want %q", path, got, wantRef)
		}
	}
}

func TestBuildRefFromMap(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]interface{}
		want string
	}{
		{"registry-repo-tag", map[string]interface{}{
			"registry":   "quay.io",
			"repository": "org/app",
			"tag":        "v1",
		}, "quay.io/org/app:v1"},
		{"repo-digest-with-prefix", map[string]interface{}{
			"repository": "ghcr.io/org/app",
			"digest":     "sha256:abc",
		}, "ghcr.io/org/app@sha256:abc"},
		{"repo-digest-without-prefix", map[string]interface{}{
			"repository": "ghcr.io/org/app",
			"digest":     "abc",
		}, "ghcr.io/org/app@sha256:abc"},
		{"registry-repo-only", map[string]interface{}{
			"registry":   "quay.io",
			"repository": "org/app",
		}, "quay.io/org/app"},
		{"repo-only", map[string]interface{}{
			"repository": "ghcr.io/org/app",
		}, "ghcr.io/org/app"},
		{"missing-repo", map[string]interface{}{
			"registry": "quay.io",
			"tag":      "v1",
		}, ""},
		{"digest-wins-over-tag", map[string]interface{}{
			"repository": "ghcr.io/org/app",
			"tag":        "v1",
			"digest":     "sha256:abc",
		}, "ghcr.io/org/app@sha256:abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRefFromMap(tc.in)
			if got != tc.want {
				t.Errorf("buildRefFromMap(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalRepo(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"docker-hub-implicit", "nginx", "index.docker.io/library/nginx"},
		{"docker-hub-implicit-tag", "nginx:1.25", "index.docker.io/library/nginx"},
		{"docker-hub-org", "library/nginx", "index.docker.io/library/nginx"},
		{"ghcr-with-tag", "ghcr.io/org/app:v1", "ghcr.io/org/app"},
		{"ghcr-with-digest", "ghcr.io/org/app@sha256:" +
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			"ghcr.io/org/app"},
		{"port-in-registry", "localhost:5000/img:v1", "localhost:5000/img"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalRepo(tc.in)
			if got != tc.want {
				t.Errorf("canonicalRepo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMatchCandidates(t *testing.T) {
	candidates := []imageCandidate{
		{Path: "image", Ref: "ghcr.io/org/app:v1"},
		{Path: "alt.image", Ref: "ghcr.io/org/app"}, // same repo, no tag — should still match
		{Path: "side.image", Ref: "ghcr.io/org/side:0.1"},
		{Path: "unrelated", Ref: "quay.io/other/thing:1"},
	}
	images := []string{
		"ghcr.io/org/app:v2",   // tag differs from candidates, but repo matches
		"ghcr.io/org/side:0.1", // exact match
		"ghcr.io/org/missing:1",
	}

	got := matchCandidates(images, candidates)

	wantPaths := map[string][]string{
		"ghcr.io/org/app:v2":     {"alt.image", "image"},
		"ghcr.io/org/side:0.1":   {"side.image"},
		"ghcr.io/org/missing:1":  nil,
	}
	for ref, want := range wantPaths {
		var paths []string
		for _, c := range got[ref] {
			paths = append(paths, c.Path)
		}
		sort.Strings(paths)
		if diff := cmp.Diff(want, paths, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("paths for %q (-want +got):\n%s", ref, diff)
		}
	}
}

func pathsOf(cs []imageCandidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}
