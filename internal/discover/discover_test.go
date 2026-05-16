package discover

import (
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

func TestFilterExcluded(t *testing.T) {
	imgs := func(refs ...string) []lockfile.Image {
		out := make([]lockfile.Image, len(refs))
		for i, r := range refs {
			out[i] = lockfile.Image{Ref: r}
		}
		return out
	}
	refsOf := func(in []lockfile.Image) []string {
		out := make([]string, len(in))
		for i, img := range in {
			out[i] = img.Ref
		}
		return out
	}

	cases := []struct {
		name    string
		images  []lockfile.Image
		exclude []chartfile.ExcludeImage
		want    []string
	}{
		{
			name:   "no exclude is identity",
			images: imgs("ghcr.io/prometheus-community/windows-exporter:v0.30.0"),
			want:   []string{"ghcr.io/prometheus-community/windows-exporter:v0.30.0"},
		},
		{
			name: "exact repo match drops, tag/digest on the ref ignored",
			images: imgs(
				"ghcr.io/prometheus-community/windows-exporter@sha256:1111111111111111111111111111111111111111111111111111111111111111",
				"quay.io/prometheus/node-exporter:v1.8.2",
			),
			exclude: []chartfile.ExcludeImage{
				{Repo: "ghcr.io/prometheus-community/windows-exporter", Reason: "Linux-only cluster"},
			},
			want: []string{"quay.io/prometheus/node-exporter:v1.8.2"},
		},
		{
			name:   "match is canonical — docker-hub implicit prefix normalizes both sides",
			images: imgs("nginx:1.27"),
			exclude: []chartfile.ExcludeImage{
				{Repo: "docker.io/library/nginx", Reason: "served from a different mirror"},
			},
			want: nil,
		},
		{
			name:   "multiple excludes, only matches dropped",
			images: imgs("quay.io/org/x:1", "registry.k8s.io/org/y:2", "ghcr.io/org/z:3"),
			exclude: []chartfile.ExcludeImage{
				{Repo: "quay.io/org/x", Reason: "r1"},
				{Repo: "ghcr.io/org/z", Reason: "r2"},
			},
			want: []string{"registry.k8s.io/org/y:2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := refsOf(filterExcluded(tc.images, tc.exclude))
			if len(got) != len(tc.want) {
				t.Fatalf("filterExcluded() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("filterExcluded()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
