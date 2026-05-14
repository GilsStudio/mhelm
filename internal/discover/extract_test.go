package discover

import (
	"sort"
	"testing"

	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var sortByRef = cmpopts.SortSlices(func(a, b candidate) bool { return a.Ref < b.Ref })

func TestExtractFromContainers(t *testing.T) {
	cases := []struct {
		name string
		doc  map[string]any
		want []candidate
	}{
		{
			name: "containers-and-initContainers",
			doc: map[string]any{
				"kind": "Deployment",
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"initContainers": []any{
								map[string]any{"image": "registry.io/init:1"},
							},
							"containers": []any{
								map[string]any{"image": "registry.io/app:2"},
								map[string]any{"image": "registry.io/sidecar:3"},
							},
						},
					},
				},
			},
			want: []candidate{
				{Ref: "registry.io/init:1", Source: lockfile.SourceManifest, Trusted: true},
				{Ref: "registry.io/app:2", Source: lockfile.SourceManifest, Trusted: true},
				{Ref: "registry.io/sidecar:3", Source: lockfile.SourceManifest, Trusted: true},
			},
		},
		{
			name: "image-key-outside-containers-ignored",
			doc: map[string]any{
				"metadata": map[string]any{"image": "registry.io/should-skip:1"},
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"image": "registry.io/keep:1"},
					},
				},
			},
			want: []candidate{
				{Ref: "registry.io/keep:1", Source: lockfile.SourceManifest, Trusted: true},
			},
		},
		{
			name: "empty-image-skipped",
			doc: map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"image": ""},
						map[string]any{"image": "registry.io/keep:1"},
					},
				},
			},
			want: []candidate{
				{Ref: "registry.io/keep:1", Source: lockfile.SourceManifest, Trusted: true},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFromContainers(tc.doc)
			if diff := cmp.Diff(tc.want, got, sortByRef); diff != "" {
				t.Errorf("extractFromContainers mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractFromEnv(t *testing.T) {
	cases := []struct {
		name string
		doc  map[string]any
		want []candidate
	}{
		{
			name: "operator-env-defaults",
			doc: map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"image": "registry.io/operator:1",
									"env": []any{
										map[string]any{"name": "CHILD_IMAGE", "value": "registry.io/child:2"},
										map[string]any{"name": "OTHER", "value": "not-an-image"},
									},
								},
							},
						},
					},
				},
			},
			want: []candidate{
				{Ref: "registry.io/child:2", Source: lockfile.SourceEnv},
			},
		},
		{
			name: "env-on-non-container-parent-ignored",
			doc: map[string]any{
				"spec": map[string]any{
					"env": []any{
						map[string]any{"name": "X", "value": "registry.io/should-skip:1"},
					},
				},
			},
			want: nil,
		},
		{
			name: "env-value-without-slash-filtered",
			doc: map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"env": []any{
								map[string]any{"name": "BAD", "value": "nginx:1"},
							},
						},
					},
				},
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFromEnv(tc.doc)
			if diff := cmp.Diff(tc.want, got, sortByRef); diff != "" {
				t.Errorf("extractFromEnv mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractFromConfigMap(t *testing.T) {
	cases := []struct {
		name string
		doc  map[string]any
		want []candidate
	}{
		{
			name: "configmap-with-multiple-image-refs",
			doc: map[string]any{
				"kind": "ConfigMap",
				"data": map[string]any{
					"ROOK_CEPH_IMAGE":     "quay.io/ceph/ceph:v18",
					"ROOK_CSI_PROVISIONER": "registry.k8s.io/sig-storage/csi-provisioner:v4",
					"some-config":         "log_level=debug",
					"empty":               "",
				},
			},
			want: []candidate{
				{Ref: "quay.io/ceph/ceph:v18", Source: lockfile.SourceConfigMap},
				{Ref: "registry.k8s.io/sig-storage/csi-provisioner:v4", Source: lockfile.SourceConfigMap},
			},
		},
		{
			name: "non-configmap-kind-returns-nil",
			doc: map[string]any{
				"kind": "Deployment",
				"data": map[string]any{"X": "registry.io/foo:1"},
			},
			want: nil,
		},
		{
			name: "configmap-with-no-data",
			doc: map[string]any{"kind": "ConfigMap"},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFromConfigMap(tc.doc)
			if diff := cmp.Diff(tc.want, got, sortByRef); diff != "" {
				t.Errorf("extractFromConfigMap mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractFromCRDSpec(t *testing.T) {
	// extractFromCRDSpec walks every string. The crane.Digest validation
	// step downstream is what drops slash-containing strings that aren't
	// real images (e.g. apiVersion, label selectors). So a "real image"
	// and an "apiVersion-shaped string" both appear here — the test pins
	// the breadth on purpose.
	doc := map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephCluster",
		"spec": map[string]any{
			"cephVersion": map[string]any{
				"image": "quay.io/ceph/ceph:v18",
			},
			"nested": map[string]any{
				"deep": []any{
					"registry.io/some-tool:v2",
					"not-an-image",
					"too-short",
				},
			},
		},
	}
	got := extractFromCRDSpec(doc)
	want := []candidate{
		{Ref: "ceph.rook.io/v1", Source: lockfile.SourceCRDSpec},
		{Ref: "quay.io/ceph/ceph:v18", Source: lockfile.SourceCRDSpec},
		{Ref: "registry.io/some-tool:v2", Source: lockfile.SourceCRDSpec},
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Ref < got[j].Ref })
	sort.Slice(want, func(i, j int) bool { return want[i].Ref < want[j].Ref })
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("extractFromCRDSpec mismatch (-want +got):\n%s", diff)
	}
}
