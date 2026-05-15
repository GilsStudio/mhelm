package discover

import (
	"sort"
	"testing"

	"github.com/gilsstudio/mhelm/internal/lockfile"
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

func TestMatchCandidates_SuffixFallback(t *testing.T) {
	candidates := []imageCandidate{
		{Path: "operator.image", Ref: "quay.io/cilium/operator"},
	}
	got := matchCandidates([]string{
		"quay.io/cilium/operator-generic:v1.19.3", // suffix-extends → match
		"quay.io/cilium/unrelated:1",              // no relation → no match
	}, candidates)

	g := got["quay.io/cilium/operator-generic:v1.19.3"]
	if len(g) != 1 || g[0].Path != "operator.image" || g[0].Accuracy != lockfile.AccuracySuffix {
		t.Errorf("operator-generic match = %+v, want one operator.image @ suffix-heuristic", g)
	}
	if len(got["quay.io/cilium/unrelated:1"]) != 0 {
		t.Errorf("unrelated should not match: %+v", got["quay.io/cilium/unrelated:1"])
	}
}

func TestSuffixExtends(t *testing.T) {
	cases := []struct {
		cr, ir string
		want   bool
	}{
		{"quay.io/cilium/operator", "quay.io/cilium/operator-generic", true},
		{"quay.io/cilium/operator", "quay.io/cilium/operator", false},   // equal
		{"quay.io/cilium/operator", "quay.io/cilium/operatorx", false},  // no hyphen
		{"quay.io/cilium/operator", "quay.io/cilium/operator-", false},  // empty token
		{"quay.io/cilium/operator", "quay.io/other/operator-generic", false}, // parent differs
		{"a.io/x/op", "b.io/x/op-generic", false},                       // registry differs
	}
	for _, c := range cases {
		if got := suffixExtends(c.cr, c.ir); got != c.want {
			t.Errorf("suffixExtends(%q,%q) = %v, want %v", c.cr, c.ir, got, c.want)
		}
	}
}

func TestMergeKey(t *testing.T) {
	cases := []struct{ ref, want string }{
		{"quay.io/cilium/operator-generic:v1.19.3", "quay.io/cilium/operator-generic:v1.19.3"},
		{"quay.io/cilium/operator-generic:v1.19.3@sha256:abc", "quay.io/cilium/operator-generic:v1.19.3"},
		{"quay.io/cilium/cilium@sha256:xyz", "quay.io/cilium/cilium"},
		{"not a ref", "not a ref"},
	}
	for _, c := range cases {
		if got := mergeKey(c.ref); got != c.want {
			t.Errorf("mergeKey(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
	// operator and operator-generic must NOT collapse.
	if mergeKey("quay.io/cilium/operator:v1") == mergeKey("quay.io/cilium/operator-generic:v1") {
		t.Error("operator and operator-generic collapsed — must stay distinct")
	}
}

func pathsOf(cs []imageCandidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}
