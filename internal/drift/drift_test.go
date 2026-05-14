package drift

import (
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-cmp/cmp"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/repo"
)

func newIndex(chartName string, entries ...chartEntry) *repo.IndexFile {
	idx := repo.NewIndexFile()
	for _, e := range entries {
		idx.Entries[chartName] = append(idx.Entries[chartName], &repo.ChartVersion{
			Metadata: &chart.Metadata{Name: chartName, Version: e.version},
			Digest:   e.digest,
		})
	}
	return idx
}

type chartEntry struct{ version, digest string }

func TestCompareChartRepoDigest(t *testing.T) {
	cf := chartfile.File{Upstream: chartfile.Endpoint{
		Type: chartfile.TypeRepo, Name: "tinychart", Version: "0.1.0",
		URL: "https://example.com/charts",
	}}

	t.Run("match-returns-nil", func(t *testing.T) {
		idx := newIndex("tinychart", chartEntry{version: "0.1.0", digest: "abc123"})
		lf := lockfile.File{Upstream: lockfile.Upstream{ChartContentDigest: "sha256:abc123"}}
		got := compareChartRepoDigest(idx, cf, lf)
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("digest-mismatch-returns-rotation", func(t *testing.T) {
		idx := newIndex("tinychart", chartEntry{version: "0.1.0", digest: "newdigest"})
		lf := lockfile.File{Upstream: lockfile.Upstream{ChartContentDigest: "sha256:abc123"}}
		got := compareChartRepoDigest(idx, cf, lf)
		want := []lockfile.DriftFinding{{
			Kind:     lockfile.DriftKindUpstreamRotation,
			Subject:  "tinychart@0.1.0",
			Expected: "sha256:abc123",
			Actual:   "sha256:newdigest",
			Note:     "upstream index.yaml now reports a different digest for this chart version",
		}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("missing-from-index-returns-upstream-missing", func(t *testing.T) {
		idx := newIndex("tinychart", chartEntry{version: "0.2.0", digest: "x"})
		lf := lockfile.File{Upstream: lockfile.Upstream{ChartContentDigest: "sha256:abc"}}
		got := compareChartRepoDigest(idx, cf, lf)
		want := []lockfile.DriftFinding{{
			Kind:    lockfile.DriftKindUpstreamMissing,
			Subject: "tinychart@0.1.0",
			Note:    "upstream index.yaml no longer lists this chart version",
		}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("empty-expected-digest-skips-comparison", func(t *testing.T) {
		// Lockfiles from a version of mhelm that didn't record the digest
		// should not produce spurious rotation findings.
		idx := newIndex("tinychart", chartEntry{version: "0.1.0", digest: "anything"})
		lf := lockfile.File{Upstream: lockfile.Upstream{ChartContentDigest: ""}}
		got := compareChartRepoDigest(idx, cf, lf)
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("digest-comparison-is-case-insensitive", func(t *testing.T) {
		idx := newIndex("tinychart", chartEntry{version: "0.1.0", digest: "ABC123"})
		lf := lockfile.File{Upstream: lockfile.Upstream{ChartContentDigest: "sha256:abc123"}}
		got := compareChartRepoDigest(idx, cf, lf)
		if got != nil {
			t.Errorf("got %+v, want nil (case-insensitive match)", got)
		}
	})
}

func TestCompareImageDigest(t *testing.T) {
	t.Run("match-returns-nil", func(t *testing.T) {
		got := compareImageDigest("r/a:1", "sha256:x", "sha256:x")
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
	t.Run("mismatch-returns-rotation", func(t *testing.T) {
		got := compareImageDigest("r/a:1", "sha256:x", "sha256:y")
		want := &lockfile.DriftFinding{
			Kind:     lockfile.DriftKindUpstreamRotation,
			Subject:  "r/a:1",
			Expected: "sha256:x",
			Actual:   "sha256:y",
			Note:     "upstream image manifest digest changed under the same ref",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})
}

func TestCompareChartOCIDigest(t *testing.T) {
	lf := lockfile.File{Upstream: lockfile.Upstream{OCIManifestDigest: "sha256:x"}}
	if got := compareChartOCIDigest("r/c:1", "sha256:x", lf); got != nil {
		t.Errorf("match: got %+v, want nil", got)
	}
	got := compareChartOCIDigest("r/c:1", "sha256:y", lf)
	if len(got) != 1 || got[0].Kind != lockfile.DriftKindUpstreamRotation {
		t.Errorf("mismatch: got %+v, want one upstream-rotation finding", got)
	}
}

func TestCompareChartDownstreamDigest(t *testing.T) {
	if got := compareChartDownstreamDigest("r/c:1", "sha256:x", "sha256:x"); got != nil {
		t.Errorf("match: got %+v, want nil", got)
	}
	got := compareChartDownstreamDigest("r/c:1", "sha256:x", "sha256:y")
	if len(got) != 1 || got[0].Kind != lockfile.DriftKindDownstreamTampered {
		t.Errorf("mismatch: got %+v, want one downstream-tampered finding", got)
	}
}

func TestCompareNewVersions(t *testing.T) {
	t.Run("nothing-newer", func(t *testing.T) {
		got := compareNewVersions("1.2.0", "tinychart", []string{"1.0.0", "1.1.0", "1.2.0"})
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("picks-latest-stable", func(t *testing.T) {
		got := compareNewVersions("1.2.0", "tinychart", []string{
			"1.0.0", "1.2.0", "1.3.0", "1.4.0", "2.0.0",
		})
		if len(got) != 1 {
			t.Fatalf("got %d findings, want 1", len(got))
		}
		if got[0].Actual != "2.0.0" {
			t.Errorf("Actual = %q, want %q", got[0].Actual, "2.0.0")
		}
		if got[0].Subject != "tinychart" {
			t.Errorf("Subject = %q, want %q", got[0].Subject, "tinychart")
		}
		// Note: "4 newer release(s) available; latest is 2.0.0"
		// (1.2.0 is filtered out as not greater; 1.3.0/1.4.0/2.0.0/... etc)
	})

	t.Run("prereleases-filtered", func(t *testing.T) {
		got := compareNewVersions("1.2.0", "tinychart", []string{
			"1.3.0-rc.1", "1.3.0-rc.2", "2.0.0-alpha.1",
		})
		if got != nil {
			t.Errorf("got %+v, want nil (all prereleases)", got)
		}
	})

	t.Run("prereleases-not-counted-toward-stable-latest", func(t *testing.T) {
		got := compareNewVersions("1.2.0", "tinychart", []string{
			"1.3.0", "1.3.1-rc.1", "1.4.0", "2.0.0-rc.1",
		})
		if len(got) != 1 {
			t.Fatalf("got %d findings, want 1", len(got))
		}
		if got[0].Actual != "1.4.0" {
			t.Errorf("Actual = %q, want %q (2.0.0-rc.1 excluded as prerelease)", got[0].Actual, "1.4.0")
		}
	})

	t.Run("invalid-current-version-returns-nil", func(t *testing.T) {
		got := compareNewVersions("not-a-semver", "tinychart", []string{"1.0.0"})
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("invalid-tags-ignored", func(t *testing.T) {
		got := compareNewVersions("1.2.0", "tinychart", []string{
			"latest", "main", "1.3.0",
		})
		if len(got) != 1 || got[0].Actual != "1.3.0" {
			t.Errorf("got %+v, want one finding for 1.3.0", got)
		}
	})

	t.Run("note-pluralizes-count", func(t *testing.T) {
		got := compareNewVersions("1.0.0", "tinychart", []string{"1.1.0", "1.2.0", "1.3.0"})
		if len(got) != 1 {
			t.Fatalf("got %d, want 1", len(got))
		}
		want := "3 newer release(s) available; latest is 1.3.0"
		if got[0].Note != want {
			t.Errorf("Note = %q, want %q", got[0].Note, want)
		}
	})
}
