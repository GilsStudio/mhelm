package chartfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validRepoFile() File {
	return File{
		APIVersion: APIVersion,
		Mirror: Mirror{
			Upstream:   Endpoint{Type: TypeRepo, Name: "ingress-nginx", URL: "https://kubernetes.github.io/ingress-nginx", Version: "4.10.0"},
			Downstream: Endpoint{Type: TypeOCI, URL: "oci://ghcr.io/mirror/ingress-nginx"},
		},
	}
}

func validOCIFile() File {
	return File{
		APIVersion: APIVersion,
		Mirror: Mirror{
			Upstream:   Endpoint{Type: TypeOCI, URL: "oci://ghcr.io/org/charts/cert-manager", Version: "1.14.0"},
			Downstream: Endpoint{Type: TypeOCI, URL: "oci://ghcr.io/mirror/cert-manager"},
		},
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*File)
		wantErrIn string // substring expected in error, "" means no error
	}{
		{"valid-repo", nil, ""},
		{"valid-oci", func(f *File) { *f = validOCIFile() }, ""},
		{"missing-upstream-type", func(f *File) { f.Mirror.Upstream.Type = "" }, "mirror.upstream.type is required"},
		{"invalid-upstream-type", func(f *File) { f.Mirror.Upstream.Type = "git" }, `mirror.upstream.type "git" invalid`},
		{"repo-missing-name", func(f *File) { f.Mirror.Upstream.Name = "" }, "mirror.upstream.name is required"},
		{"oci-missing-oci-prefix", func(f *File) {
			*f = validOCIFile()
			f.Mirror.Upstream.URL = "ghcr.io/org/charts/cert-manager"
		}, "mirror.upstream.url must start with oci://"},
		{"missing-upstream-url", func(f *File) { f.Mirror.Upstream.URL = "" }, "mirror.upstream.url is required"},
		{"missing-upstream-version", func(f *File) { f.Mirror.Upstream.Version = "" }, "mirror.upstream.version is required"},
		{"downstream-type-not-oci", func(f *File) { f.Mirror.Downstream.Type = "repo" }, `mirror.downstream.type must be "oci"`},
		{"downstream-missing-oci-prefix", func(f *File) {
			f.Mirror.Downstream.URL = "ghcr.io/mirror/x"
		}, "mirror.downstream.url must start with oci://"},
		{"extra-image-missing-ref", func(f *File) {
			f.Mirror.ExtraImages = []ExtraImage{{Ref: "registry.io/ok:1"}, {Ref: ""}}
		}, "mirror.extraImages[1].ref is required"},
		{"vuln-failon-invalid", func(f *File) {
			f.Mirror.VulnPolicy = &VulnPolicy{FailOn: "bogus"}
		}, `mirror.vulnPolicy.failOn "bogus" invalid`},
		{"vuln-allowlist-missing-cve", func(f *File) {
			f.Mirror.VulnPolicy = &VulnPolicy{
				Allowlist: []VulnWaiver{{CVE: "", Expires: "2030-01-01", Reason: "x"}},
			}
		}, "mirror.vulnPolicy.allowlist[0].cve is required"},
		{"vuln-allowlist-bad-expires", func(f *File) {
			f.Mirror.VulnPolicy = &VulnPolicy{
				Allowlist: []VulnWaiver{{CVE: "CVE-1", Expires: "yesterday", Reason: "x"}},
			}
		}, "mirror.vulnPolicy.allowlist[0].expires"},
		{"vuln-allowlist-ok", func(f *File) {
			f.Mirror.VulnPolicy = &VulnPolicy{
				FailOn:    FailOnHigh,
				Allowlist: []VulnWaiver{{CVE: "CVE-2024-1", Expires: "2030-01-01", Reason: "tracked"}},
			}
		}, ""},
		{"apiversion-invalid", func(f *File) { f.APIVersion = "mhelm.io/v999" }, `apiVersion "mhelm.io/v999" invalid`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := validRepoFile()
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
			in: File{Mirror: Mirror{Upstream: Endpoint{
				Type: TypeRepo,
				Name: "ingress-nginx",
				URL:  "https://kubernetes.github.io/ingress-nginx",
			}}},
			want: "ingress-nginx",
		},
		{
			name: "oci-uses-path-base",
			in: File{Mirror: Mirror{Upstream: Endpoint{
				Type: TypeOCI,
				URL:  "oci://ghcr.io/org/charts/cert-manager",
			}}},
			want: "cert-manager",
		},
		{
			name: "oci-trailing-slash-not-expected-but-handled",
			in: File{Mirror: Mirror{Upstream: Endpoint{
				Type: TypeOCI,
				URL:  "oci://ghcr.io/org/charts/cert-manager/",
			}}},
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

// TestLoadMigratesV01 covers the v0.1.0 flat-shape auto-migration. The
// file on disk is intentionally NOT rewritten — we only normalize in
// memory and warn to stderr.
func TestLoadMigratesV01(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chart.json")

	legacy := `{
  "upstream":   {"type": "repo", "name": "ingress-nginx", "url": "https://k/i", "version": "1.0.0"},
  "downstream": {"type": "oci", "url": "oci://ghcr.io/mirror/i"},
  "valuesFiles": ["values.yml"],
  "extraImages": [{"ref": "ghcr.io/x/y:1"}],
  "trustedIdentities": [{"subjectRegex": "https://github.com/.*", "issuer": "https://token.actions.githubusercontent.com"}]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.APIVersion != APIVersion {
		t.Errorf("APIVersion = %q, want %q", f.APIVersion, APIVersion)
	}
	if f.Mirror.Upstream.Name != "ingress-nginx" {
		t.Errorf("mirror.upstream.name not migrated: %q", f.Mirror.Upstream.Name)
	}
	if f.Mirror.Downstream.URL != "oci://ghcr.io/mirror/i" {
		t.Errorf("mirror.downstream.url not migrated: %q", f.Mirror.Downstream.URL)
	}
	if len(f.Mirror.ExtraImages) != 1 || f.Mirror.ExtraImages[0].Ref != "ghcr.io/x/y:1" {
		t.Errorf("mirror.extraImages not migrated: %+v", f.Mirror.ExtraImages)
	}
	if len(f.Mirror.Verify.TrustedIdentities) != 1 {
		t.Errorf("mirror.verify.trustedIdentities not migrated: %+v", f.Mirror.Verify.TrustedIdentities)
	}
	if f.Wrap == nil || len(f.Wrap.ValuesFiles) != 1 || f.Wrap.ValuesFiles[0] != "values.yml" {
		t.Errorf("wrap.valuesFiles not migrated: %+v", f.Wrap)
	}

	// File on disk must NOT have been rewritten.
	on, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(on) != legacy {
		t.Errorf("file was rewritten; expected v0.1.0 bytes preserved\n--- before\n%s\n--- after\n%s", legacy, on)
	}
}

func TestLoadV1alpha1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chart.json")
	want := File{
		APIVersion: APIVersion,
		Mirror: Mirror{
			Upstream:        Endpoint{Type: TypeRepo, Name: "x", URL: "https://x", Version: "1.0"},
			Downstream:      Endpoint{Type: TypeOCI, URL: "oci://ghcr.io/x"},
			DiscoveryValues: []string{"a.yml"},
			Verify:          Verify{AllowUnsigned: []string{"cilium/hubble-ui"}},
			VulnPolicy: &VulnPolicy{
				FailOn:    FailOnHigh,
				Allowlist: []VulnWaiver{{CVE: "CVE-1", Expires: "2030-01-01", Reason: "tracked"}},
			},
		},
	}
	b, _ := json.MarshalIndent(want, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.APIVersion != APIVersion {
		t.Errorf("APIVersion = %q", got.APIVersion)
	}
	if got.Mirror.VulnPolicy == nil || got.Mirror.VulnPolicy.FailOn != FailOnHigh {
		t.Errorf("vulnPolicy not parsed: %+v", got.Mirror.VulnPolicy)
	}
	if len(got.Mirror.Verify.AllowUnsigned) != 1 {
		t.Errorf("allowUnsigned not parsed: %+v", got.Mirror.Verify.AllowUnsigned)
	}
}

func TestDiscoveryValuesEffective_PrefersMirror(t *testing.T) {
	f := File{
		Mirror: Mirror{DiscoveryValues: []string{"discovery.yml"}},
		Wrap:   &Wrap{ValuesFiles: []string{"deploy.yml"}},
	}
	got := f.DiscoveryValuesEffective()
	if len(got) != 1 || got[0] != "discovery.yml" {
		t.Errorf("got %v, want [discovery.yml]", got)
	}
}

// TestDiscoveryValuesEffective_NoFallback locks in the v0.3.0 sunset:
// wrap.valuesFiles is no longer a discovery input. Setting it without
// mirror.discoveryValues yields an empty discovery list — `mhelm
// discover` renders with chart defaults only.
func TestDiscoveryValuesEffective_NoFallback(t *testing.T) {
	f := File{Wrap: &Wrap{Name: "x", Version: "1", ValuesFiles: []string{"deploy.yml"}}}
	got := f.DiscoveryValuesEffective()
	if len(got) != 0 {
		t.Errorf("got %v, want empty (wrap.valuesFiles bridge sunset in v0.3.0)", got)
	}
}

// TestValidate_ReleaseRequiresNameAndNamespace locks in the v0.4.0
// rule: a non-nil release section must carry both fields.
func TestValidate_ReleaseRequiresNameAndNamespace(t *testing.T) {
	t.Run("name-missing", func(t *testing.T) {
		f := validRepoFile()
		f.Release = &Release{Namespace: "kube-system"}
		err := f.Validate()
		if err == nil || !strings.Contains(err.Error(), "release.name") {
			t.Errorf("Validate() = %v, want release.name error", err)
		}
	})
	t.Run("namespace-missing", func(t *testing.T) {
		f := validRepoFile()
		f.Release = &Release{Name: "cilium"}
		err := f.Validate()
		if err == nil || !strings.Contains(err.Error(), "release.namespace") {
			t.Errorf("Validate() = %v, want release.namespace error", err)
		}
	})
	t.Run("both-present-ok", func(t *testing.T) {
		f := validRepoFile()
		f.Release = &Release{Name: "cilium", Namespace: "kube-system", ValuesFiles: []string{"x.yml"}}
		if err := f.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})
}

// TestValidate_WrapRequiresNameAndVersion locks in the v0.3.0 rule:
// a non-nil wrap section must carry both wrap.name and wrap.version.
func TestValidate_WrapRequiresNameAndVersion(t *testing.T) {
	t.Run("name-missing", func(t *testing.T) {
		f := validRepoFile()
		f.Wrap = &Wrap{Version: "1.0"}
		err := f.Validate()
		if err == nil || !strings.Contains(err.Error(), "wrap.name") {
			t.Errorf("Validate() = %v, want wrap.name error", err)
		}
	})
	t.Run("version-missing", func(t *testing.T) {
		f := validRepoFile()
		f.Wrap = &Wrap{Name: "wrapper"}
		err := f.Validate()
		if err == nil || !strings.Contains(err.Error(), "wrap.version") {
			t.Errorf("Validate() = %v, want wrap.version error", err)
		}
	})
	t.Run("both-present-ok", func(t *testing.T) {
		f := validRepoFile()
		f.Wrap = &Wrap{Name: "wrapper", Version: "1.0"}
		if err := f.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})
}
