package discover

import (
	"sort"
	"strings"
	"sync"

	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
)

// validateAndDedupe takes raw candidates from every extractor, resolves
// each ref's manifest digest concurrently against the registry, and
// returns one lockfile.Image per unique ref. Trusted candidates (manifest
// + manual) survive a resolution failure; untrusted regex sources don't.
//
// When the same ref shows up from multiple sources, the highest-trust
// source wins per sourceRank.
func validateAndDedupe(cands []candidate) []lockfile.Image {
	merged := mergeCandidates(cands)
	return resolveDigests(merged)
}

// mergeCandidates deduplicates candidates by canonical identity
// (mergeKey: repo[:tag], digest ignored) so a manual extraImages entry
// and the rendered manifest entry for the same image — which differ only
// by an appended @sha256 — collapse into one row. The highest-trust
// source wins per sourceRank; on a rank tie the digest-bearing ref wins
// (it is the more specific identity). Trusted=true if any contributing
// candidate was trusted. Pure logic — no network access.
func mergeCandidates(cands []candidate) []candidate {
	merged := map[string]candidate{}
	for _, c := range cands {
		k := mergeKey(c.Ref)
		prev, ok := merged[k]
		if !ok {
			merged[k] = c
			continue
		}
		winner := prev
		switch {
		case sourceRank(c.Source) < sourceRank(prev.Source):
			winner = c
		case sourceRank(c.Source) == sourceRank(prev.Source) && hasDigest(c.Ref) && !hasDigest(prev.Ref):
			winner = c
		}
		winner.Trusted = prev.Trusted || c.Trusted
		merged[k] = winner
	}
	out := make([]candidate, 0, len(merged))
	for _, c := range merged {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// mergeKey is the canonical repo[:tag] with any @sha256 digest stripped.
// Refs that differ only by an appended digest collapse to the same key;
// different repositories (operator vs operator-generic) or tags stay
// distinct. Falls back to the lowered raw ref when it cannot be parsed —
// never silently collapses unparseable refs.
func mergeKey(ref string) string {
	bare := ref
	if i := strings.Index(bare, "@"); i >= 0 {
		bare = bare[:i]
	}
	// Explicit tag = a ':' after the last '/'. A digest-only ref
	// (repo@sha256:..) must key to the bare repo, NOT repo:latest, or it
	// would wrongly collapse with a `repo:latest` tagged image.
	tag := ""
	if i := strings.LastIndex(bare, ":"); i >= 0 && !strings.Contains(bare[i+1:], "/") {
		tag = bare[i+1:]
		bare = bare[:i]
	}
	r, err := name.NewRepository(bare)
	if err != nil {
		return strings.ToLower(ref)
	}
	if tag != "" {
		return r.Name() + ":" + tag
	}
	return r.Name()
}

func hasDigest(ref string) bool { return strings.Contains(ref, "@sha256:") }

// resolveDigests HEADs each candidate against its registry in parallel and
// builds the final lockfile.Image slice. Untrusted candidates whose digest
// can't be resolved are dropped.
func resolveDigests(cands []candidate) []lockfile.Image {
	type result struct {
		c      candidate
		digest string
		ok     bool
	}
	results := make([]result, len(cands))
	for i, c := range cands {
		results[i] = result{c: c}
	}

	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			d, err := crane.Digest(results[i].c.Ref)
			if err == nil {
				results[i].digest = d
				results[i].ok = true
			}
		}(i)
	}
	wg.Wait()

	var out []lockfile.Image
	for _, r := range results {
		if !r.ok && !r.c.Trusted {
			continue
		}
		out = append(out, lockfile.Image{
			Ref:           r.c.Ref,
			Digest:        r.digest,
			Source:        r.c.Source,
			DiscoveredVia: r.c.DiscoveredVia,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// sourceRank gives lower numbers to more trusted sources. Used as the
// tie-breaker when multiple extractors yield the same ref.
func sourceRank(s string) int {
	switch s {
	case lockfile.SourceManifest:
		return 0
	case lockfile.SourceAnnotation:
		return 1
	case lockfile.SourceManual:
		return 2
	case lockfile.SourceEnv:
		return 3
	case lockfile.SourceConfigMap:
		return 4
	case lockfile.SourceCRDSpec:
		return 5
	default:
		return 100
	}
}
