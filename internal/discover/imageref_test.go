package discover

import "testing"

func TestLooksLikeImageRef(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"too-short", "a/b", false},
		{"no-slash", "foo:bar", false},
		{"has-space", "registry.io/foo bar:1", false},
		{"has-dollar-template", "${REGISTRY}/foo:1", false},
		{"has-braces", "registry.io/{foo}:1", false},
		{"has-quote", "registry.io/foo\":1", false},
		{"plain-registry-tag", "registry.io/foo:1.0", true},
		{"docker-io", "docker.io/library/nginx:1.25.3", true},
		{"port-in-registry", "localhost:5000/img:v1", true},
		{"digest-only", "registry.io/foo@sha256:" +
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", true},
		{"long-but-valid", "ghcr.io/some-org/some-repo/some-image:" +
			"v1.2.3-alpha.4", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeImageRef(tc.in)
			if got != tc.want {
				t.Errorf("looksLikeImageRef(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseDocs(t *testing.T) {
	cases := []struct {
		name     string
		input    map[string]string
		wantKeys [][]string
	}{
		{
			name: "single-doc",
			input: map[string]string{
				"a.yaml": "apiVersion: v1\nkind: ConfigMap\n",
			},
			wantKeys: [][]string{{"apiVersion", "kind"}},
		},
		{
			name: "multi-doc-with-empty",
			input: map[string]string{
				"a.yaml": "apiVersion: v1\nkind: A\n---\n---\napiVersion: v1\nkind: B\n",
			},
			wantKeys: [][]string{
				{"apiVersion", "kind"},
				{"apiVersion", "kind"},
			},
		},
		{
			name: "comment-only-doc-dropped",
			input: map[string]string{
				"a.yaml": "# only a comment\n---\napiVersion: v1\nkind: ok\n",
			},
			wantKeys: [][]string{{"apiVersion", "kind"}},
		},
		{
			name: "scalar-doc-dropped",
			input: map[string]string{
				"a.yaml": "just-a-string\n---\napiVersion: v1\nkind: ok\n",
			},
			wantKeys: [][]string{{"apiVersion", "kind"}},
		},
		{
			name: "all-empty",
			input: map[string]string{
				"a.yaml": "\n---\n\n---\n",
			},
			wantKeys: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDocs(tc.input)
			if len(got) != len(tc.wantKeys) {
				t.Fatalf("got %d docs, want %d: %#v", len(got), len(tc.wantKeys), got)
			}
			for i, doc := range got {
				want := tc.wantKeys[i]
				for _, k := range want {
					if _, ok := doc[k]; !ok {
						t.Errorf("doc[%d] missing key %q (have %v)", i, k, keys(doc))
					}
				}
			}
		})
	}
}

func TestIsBuiltinKind(t *testing.T) {
	cases := []struct {
		apiVersion string
		want       bool
	}{
		{"v1", true},
		{"apps/v1", true},
		{"batch/v1", true},
		{"apiextensions.k8s.io/v1", true},
		{"cert-manager.io/v1", false},
		{"monitoring.coreos.com/v1", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.apiVersion, func(t *testing.T) {
			doc := map[string]any{"apiVersion": tc.apiVersion}
			got := isBuiltinKind(doc)
			if got != tc.want {
				t.Errorf("isBuiltinKind(%q) = %v, want %v", tc.apiVersion, got, tc.want)
			}
		})
	}
	t.Run("no-apiVersion-key", func(t *testing.T) {
		if isBuiltinKind(map[string]any{}) {
			t.Error("expected false for doc with no apiVersion")
		}
	})
}

// keys returns the map's keys for test diagnostics.
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

