package slsa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

var frozen = time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

func withFrozenClock(t *testing.T) {
	t.Helper()
	prev := now
	now = func() time.Time { return frozen }
	t.Cleanup(func() { now = prev })
}

func clearGHAEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_SHA", "GITHUB_REF",
		"GITHUB_WORKFLOW_REF", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT",
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
		Downstream: chartfile.Endpoint{Type: chartfile.TypeOCI, URL: "oci://ghcr.io/mirror/tinychart"},
	}
	lf := lockfile.File{
		Chart: lockfile.Chart{Name: "tinychart", Version: "0.1.0"},
		Upstream: lockfile.Upstream{
			Type: "repo",
			URL:  "https://example.com/charts",
			ChartContentDigest: "sha256:0123456789abcdef0123456789abcdef" +
				"0123456789abcdef0123456789abcdef",
		},
		Images: []lockfile.Image{
			{
				Ref:    "registry.io/app:1",
				Digest: "sha256:abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabca",
			},
		},
	}
	return cf, lf
}

func TestPredicateJSONShape(t *testing.T) {
	clearGHAEnv(t)
	withFrozenClock(t)

	cf, lf := canonicalInputs()
	p := Build(cf, lf, "charts/tiny", "v1.2.3")
	got, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	compareOrUpdateGolden(t, filepath.Join("testdata", "predicate.json"), got)
}

func TestPredicateJSONShape_GitHubActionsContext(t *testing.T) {
	clearGHAEnv(t)
	withFrozenClock(t)
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "org/repo")
	t.Setenv("GITHUB_SHA", "deadbeef")
	t.Setenv("GITHUB_REF", "refs/heads/main")
	t.Setenv("GITHUB_WORKFLOW_REF", "org/repo/.github/workflows/mirror.yml@refs/heads/main")
	t.Setenv("GITHUB_RUN_ID", "12345")
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")

	cf, lf := canonicalInputs()
	p := Build(cf, lf, "charts/tiny", "v1.2.3")

	if p.RunDetails.Builder.ID == "" {
		t.Error("Builder.ID empty, want populated from env")
	}
	if p.RunDetails.Metadata.InvocationID != "12345-2" {
		t.Errorf("InvocationID = %q, want %q", p.RunDetails.Metadata.InvocationID, "12345-2")
	}
	if p.BuildDefinition.ExternalParameters.Source == nil {
		t.Fatal("ExternalParameters.Source nil, want populated")
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
