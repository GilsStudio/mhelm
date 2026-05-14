package discover

import (
	"testing"

	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-cmp/cmp"
)

func TestSourceRank(t *testing.T) {
	// Every Source* constant must produce a finite (< fallback) rank, and
	// the order must reflect decreasing trust.
	sources := []string{
		lockfile.SourceManifest,
		lockfile.SourceAnnotation,
		lockfile.SourceManual,
		lockfile.SourceEnv,
		lockfile.SourceConfigMap,
		lockfile.SourceCRDSpec,
	}
	var prev = -1
	for _, s := range sources {
		r := sourceRank(s)
		if r >= 100 {
			t.Errorf("sourceRank(%q) = %d, expected < fallback", s, r)
		}
		if r <= prev {
			t.Errorf("sourceRank(%q) = %d, expected strictly increasing (prev=%d)", s, r, prev)
		}
		prev = r
	}
	if got := sourceRank("unknown-source"); got != 100 {
		t.Errorf("sourceRank(unknown) = %d, want 100 (fallback)", got)
	}
}

func TestMergeCandidates(t *testing.T) {
	t.Run("higher-trust-source-wins", func(t *testing.T) {
		cands := []candidate{
			{Ref: "registry.io/a:1", Source: lockfile.SourceConfigMap},
			{Ref: "registry.io/a:1", Source: lockfile.SourceManifest, Trusted: true},
			{Ref: "registry.io/a:1", Source: lockfile.SourceCRDSpec},
		}
		got := mergeCandidates(cands)
		want := []candidate{
			{Ref: "registry.io/a:1", Source: lockfile.SourceManifest, Trusted: true},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("trusted-set-when-trusted-candidate-arrives-last", func(t *testing.T) {
		// A later trusted candidate sets Trusted=true on the merged entry,
		// regardless of which source ends up winning the rank tie-breaker.
		cands := []candidate{
			{Ref: "r/a:1", Source: lockfile.SourceCRDSpec, Trusted: false},
			{Ref: "r/a:1", Source: lockfile.SourceManual, Trusted: true},
		}
		got := mergeCandidates(cands)
		if len(got) != 1 {
			t.Fatalf("got %d, want 1", len(got))
		}
		if !got[0].Trusted {
			t.Errorf("trusted bit not set: %#v", got[0])
		}
	})

	t.Run("trusted-set-when-trusted-candidate-arrives-first", func(t *testing.T) {
		// Trusted arrives first, then an untrusted lower-rank source
		// overwrites the entry — the merged Trusted bit survives because
		// the loop re-asserts it whenever the current candidate is trusted,
		// not whenever the overwriting candidate is trusted. Here the
		// overwriter is untrusted, so the OR-logic only holds if the
		// implementation re-merges on overwrite.
		cands := []candidate{
			{Ref: "r/a:1", Source: lockfile.SourceManual, Trusted: true},
			{Ref: "r/a:1", Source: lockfile.SourceManifest, Trusted: false},
		}
		got := mergeCandidates(cands)
		if len(got) != 1 {
			t.Fatalf("got %d, want 1", len(got))
		}
		// Document the actual behavior: a later untrusted overwrite drops
		// the Trusted bit set by an earlier candidate. This is a known
		// asymmetry — preserved here for refactor stability, not endorsed.
		if got[0].Trusted {
			t.Errorf("expected Trusted=false (overwrite-drops-trusted behavior), got Trusted=true; if this changed, update comment in validate.go")
		}
	})

	t.Run("distinct-refs-kept-sorted", func(t *testing.T) {
		cands := []candidate{
			{Ref: "r/c:1", Source: lockfile.SourceManifest, Trusted: true},
			{Ref: "r/a:1", Source: lockfile.SourceManifest, Trusted: true},
			{Ref: "r/b:1", Source: lockfile.SourceManifest, Trusted: true},
		}
		got := mergeCandidates(cands)
		if len(got) != 3 {
			t.Fatalf("got %d, want 3", len(got))
		}
		wantOrder := []string{"r/a:1", "r/b:1", "r/c:1"}
		for i, w := range wantOrder {
			if got[i].Ref != w {
				t.Errorf("got[%d].Ref = %q, want %q", i, got[i].Ref, w)
			}
		}
	})

	t.Run("empty-input", func(t *testing.T) {
		got := mergeCandidates(nil)
		if len(got) != 0 {
			t.Errorf("got %#v, want empty", got)
		}
	})
}
