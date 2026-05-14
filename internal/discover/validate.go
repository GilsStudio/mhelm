package discover

import (
	"sort"
	"sync"

	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/crane"
)

// validateAndDedupe takes raw candidates from every extractor, resolves
// each ref's manifest digest concurrently against the registry, and
// returns one lockfile.Image per unique ref. Trusted candidates (manifest
// + manual) survive a resolution failure; untrusted regex sources don't.
//
// When the same ref shows up from multiple sources, the highest-trust
// source wins per sourceRank.
func validateAndDedupe(cands []candidate) []lockfile.Image {
	merged := map[string]candidate{}
	for _, c := range cands {
		prev, ok := merged[c.Ref]
		if !ok || sourceRank(c.Source) < sourceRank(prev.Source) {
			merged[c.Ref] = c
		}
		// Preserve Trusted=true if any source for this ref was trusted.
		if c.Trusted {
			m := merged[c.Ref]
			m.Trusted = true
			merged[c.Ref] = m
		}
	}

	type result struct {
		c      candidate
		digest string
		ok     bool
	}
	results := make([]result, 0, len(merged))
	for _, c := range merged {
		results = append(results, result{c: c})
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
			continue // regex source that registry couldn't confirm — drop
		}
		out = append(out, lockfile.Image{
			Ref:    r.c.Ref,
			Digest: r.digest,
			Source: r.c.Source,
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
