package chartfile

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	validRepo := File{
		Upstream:   Endpoint{Type: TypeRepo, Name: "ingress-nginx", URL: "https://kubernetes.github.io/ingress-nginx", Version: "4.10.0"},
		Downstream: Endpoint{Type: TypeOCI, URL: "oci://ghcr.io/mirror/ingress-nginx"},
	}
	validOCI := File{
		Upstream:   Endpoint{Type: TypeOCI, URL: "oci://ghcr.io/org/charts/cert-manager", Version: "1.14.0"},
		Downstream: Endpoint{Type: TypeOCI, URL: "oci://ghcr.io/mirror/cert-manager"},
	}

	cases := []struct {
		name      string
		mutate    func(*File)
		wantErrIn string // substring expected in error, "" means no error
	}{
		{"valid-repo", nil, ""},
		{"valid-oci", func(f *File) { *f = validOCI }, ""},
		{"missing-upstream-type", func(f *File) { f.Upstream.Type = "" }, "upstream.type is required"},
		{"invalid-upstream-type", func(f *File) { f.Upstream.Type = "git" }, `upstream.type "git" invalid`},
		{"repo-missing-name", func(f *File) { f.Upstream.Name = "" }, "upstream.name is required"},
		{"oci-missing-oci-prefix", func(f *File) {
			*f = validOCI
			f.Upstream.URL = "ghcr.io/org/charts/cert-manager"
		}, "upstream.url must start with oci://"},
		{"missing-upstream-url", func(f *File) { f.Upstream.URL = "" }, "upstream.url is required"},
		{"missing-upstream-version", func(f *File) { f.Upstream.Version = "" }, "upstream.version is required"},
		{"downstream-type-not-oci", func(f *File) { f.Downstream.Type = "repo" }, `downstream.type must be "oci"`},
		{"downstream-missing-oci-prefix", func(f *File) {
			f.Downstream.URL = "ghcr.io/mirror/x"
		}, "downstream.url must start with oci://"},
		{"extra-image-missing-ref", func(f *File) {
			f.ExtraImages = []ExtraImage{{Ref: "registry.io/ok:1"}, {Ref: ""}}
		}, "extraImages[1].ref is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := validRepo
			if tc.mutate != nil {
				tc.mutate(&f)
			}
			err := f.Validate()
			if tc.wantErrIn == "" {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErrIn)
			}
			if !strings.Contains(err.Error(), tc.wantErrIn) {
				t.Errorf("Validate() = %q, want substring %q", err.Error(), tc.wantErrIn)
			}
		})
	}
}

func TestChartName(t *testing.T) {
	cases := []struct {
		name string
		in   File
		want string
	}{
		{
			name: "repo-uses-upstream-name",
			in: File{Upstream: Endpoint{
				Type: TypeRepo,
				Name: "ingress-nginx",
				URL:  "https://kubernetes.github.io/ingress-nginx",
			}},
			want: "ingress-nginx",
		},
		{
			name: "oci-uses-path-base",
			in: File{Upstream: Endpoint{
				Type: TypeOCI,
				URL:  "oci://ghcr.io/org/charts/cert-manager",
			}},
			want: "cert-manager",
		},
		{
			name: "oci-trailing-slash-not-expected-but-handled",
			in: File{Upstream: Endpoint{
				Type: TypeOCI,
				URL:  "oci://ghcr.io/org/charts/cert-manager/",
			}},
			// path.Base of "/.../cert-manager/" returns "cert-manager"
			want: "cert-manager",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.ChartName()
			if got != tc.want {
				t.Errorf("ChartName() = %q, want %q", got, tc.want)
			}
		})
	}
}
