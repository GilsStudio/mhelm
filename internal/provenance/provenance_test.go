package provenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

// frozen is the deterministic timestamp used in all golden tests. Any
// change here invalidates the goldens.
var frozen = time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

func withFrozenClock(t *testing.T) {
	t.Helper()
	prev := now
	now = func() time.Time { return frozen }
	t.Cleanup(func() { now = prev })
}

// clearGHAEnv clears all GitHub Actions env vars Build inspects, so the
// default golden never picks up the host runner's CI environment.
func clearGHAEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"GITHUB_REPOSITORY", "GITHUB_WORKFLOW", "GITHUB_RUN_ID",
		"GITHUB_RUN_ATTEMPT", "GITHUB_SHA", "GITHUB_REF_NAME",
	} {
		t.Setenv(v, "")
	}
}

func canonicalInputs() (chartfile.File, lockfile.File) {
	cf := chartfile.File{
		Upstream: chartfile.Endpoint{
			Type: chartfile.TypeRepo, Name: "tinychart",
			URL: "https://example.com/charts", Version: "0.1.0",
		},
		Downstream: chartfile.Endpoint{
			Type: chartfile.TypeOCI,
			URL:  "oci://ghcr.io/mirror/tinychart",
		},
	}
	lf := lockfile.File{
		Chart: lockfile.Chart{Name: "tinychart", Version: "0.1.0"},
		Upstream: lockfile.Upstream{
			Type: "repo",
			URL:  "https://example.com/charts",
			ChartContentDigest: "sha256:0123456789abcdef0123456789abcdef" +
				"0123456789abcdef0123456789abcdef",
		},
		Downstream: lockfile.Downstream{
			Ref: "ghcr.io/mirror/tinychart:0.1.0",
			OCIManifestDigest: "sha256:1111111111111111111111111111111111" +
				"11111111111111111111111111111111",
		},
		Images: []lockfile.Image{
			{
				Ref:    "registry.io/app:1",
				Digest: "sha256:abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabca",
				DownstreamRef:    "ghcr.io/mirror/app:1",
				DownstreamDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
				Signature: &lockfile.Signature{
					Verified:      true,
					Type:          "cosign-keyless",
					Subject:       "https://github.com/upstream/app/.github/workflows/release.yml@refs/heads/main",
					Issuer:        "https://token.actions.githubusercontent.com",
					RekorLogIndex: 99,
				},
			},
		},
	}
	return cf, lf
}

func TestPredicateJSONShape(t *testing.T) {
	clearGHAEnv(t)
	withFrozenClock(t)

	cf, lf := canonicalInputs()
	p := Build(cf, lf, "v1.2.3")
	got, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	goldenPath := filepath.Join("testdata", "predicate.json")
	compareOrUpdateGolden(t, goldenPath, got)
}

func TestPredicateJSONShape_GitHubActionsContext(t *testing.T) {
	clearGHAEnv(t)
	withFrozenClock(t)
	t.Setenv("GITHUB_REPOSITORY", "org/repo")
	t.Setenv("GITHUB_WORKFLOW", "mirror")
	t.Setenv("GITHUB_RUN_ID", "12345")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	t.Setenv("GITHUB_SHA", "deadbeef")
	t.Setenv("GITHUB_REF_NAME", "main")

	cf, lf := canonicalInputs()
	p := Build(cf, lf, "v1.2.3")

	if p.BuildContext == nil {
		t.Fatal("BuildContext is nil, want populated from env")
	}
	if p.BuildContext.Repository != "org/repo" {
		t.Errorf("Repository = %q", p.BuildContext.Repository)
	}
	if p.BuildContext.Workflow != "mirror" {
		t.Errorf("Workflow = %q", p.BuildContext.Workflow)
	}
	if p.BuildContext.RunID != "12345" {
		t.Errorf("RunID = %q", p.BuildContext.RunID)
	}
	if p.BuildContext.SHA != "deadbeef" {
		t.Errorf("SHA = %q", p.BuildContext.SHA)
	}
	if p.BuildContext.RefName != "main" {
		t.Errorf("RefName = %q", p.BuildContext.RefName)
	}

	got, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	compareOrUpdateGolden(t, filepath.Join("testdata", "predicate-gha.json"), got)
}

func compareOrUpdateGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if os.Getenv("MHELM_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		t.Logf("updated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with MHELM_UPDATE_GOLDEN=1 to create): %v", err)
	}
	if string(want) != string(got) {
		t.Errorf("golden mismatch at %s\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}
