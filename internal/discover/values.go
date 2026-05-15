package discover

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gilsstudio/mhelm/internal/imagevalues"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/name"
)

// scoredCandidate is a matched values candidate plus how it was matched
// (exact canonical repo → heuristic, hyphen-suffix extension → suffix).
type scoredCandidate struct {
	imageCandidate
	Accuracy string
}

// imageCandidate is one (values-path → image-ref) tuple produced by
// walking the chart's merged values. Alias for imagevalues.Candidate
// so the rewrite logic in package imagevalues can consume our matches
// directly.
type imageCandidate = imagevalues.Candidate

// findImageCandidates walks merged values for every `image:` key whose value
// is either a non-empty string or a map containing `repository`. The walk is
// purely heuristic — it does not know what the chart's templates actually do
// with these values.
func findImageCandidates(values map[string]interface{}) []imageCandidate {
	var out []imageCandidate
	walkValues(values, "", &out)
	return out
}

func walkValues(node interface{}, prefix string, out *[]imageCandidate) {
	m, ok := node.(map[string]interface{})
	if !ok {
		return
	}
	for k, child := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if k == "image" {
			switch v := child.(type) {
			case string:
				if v != "" {
					*out = append(*out, imageCandidate{Path: path, Ref: v, StringForm: v})
				}
			case map[string]interface{}:
				if ref := buildRefFromMap(v); ref != "" {
					*out = append(*out, imageCandidate{Path: path, Ref: ref, ObjectForm: v})
				}
				// Recurse — some charts nest more image refs inside an image block.
				walkValues(v, path, out)
			}
			continue
		}
		walkValues(child, path, out)
	}
}

func buildRefFromMap(m map[string]interface{}) string {
	repo := toString(m["repository"])
	if repo == "" {
		return ""
	}
	reg := toString(m["registry"])
	tag := toString(m["tag"])
	digest := toString(m["digest"])

	base := repo
	if reg != "" {
		base = reg + "/" + repo
	}
	if digest != "" {
		if !strings.HasPrefix(digest, "sha256:") {
			digest = "sha256:" + digest
		}
		return base + "@" + digest
	}
	if tag != "" {
		return base + ":" + tag
	}
	return base
}

func toString(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// matchCandidates pairs each discovered image ref with the values paths
// whose canonical repository path equals the discovered ref's. Tag/digest
// are intentionally ignored — many charts omit the tag in defaults and
// fill it from Chart.AppVersion at render time, so candidates from values
// rarely carry tags. Repository-level matching is good enough as a
// heuristic; dual-render verification in a later phase will tighten it.
// A hyphen-suffix fallback runs only when the exact pass found nothing
// for an image (so it can't introduce false positives over an exact
// match): a chart default repo `…/operator` matches a rendered image
// `…/operator-generic` (cilium concatenates the suffix at template
// time). extraImages[].valuesPath remains the authoritative override.
func matchCandidates(images []string, candidates []imageCandidate) map[string][]scoredCandidate {
	out := make(map[string][]scoredCandidate, len(images))
	for _, img := range images {
		ic := canonicalRepo(img)
		for _, c := range candidates {
			if canonicalRepo(c.Ref) == ic {
				out[img] = append(out[img], scoredCandidate{c, lockfile.AccuracyHeuristic})
			}
		}
		if len(out[img]) == 0 {
			for _, c := range candidates {
				if suffixExtends(canonicalRepo(c.Ref), ic) {
					out[img] = append(out[img], scoredCandidate{c, lockfile.AccuracySuffix})
				}
			}
		}
	}
	return out
}

// dedupeValuesPaths unions ValuesPaths by Path, keeping the most
// authoritative accuracy (manual > suffix-heuristic > heuristic) when
// the same path is contributed by several matchers. Sorted by Path so
// the lockfile bytes are stable.
func dedupeValuesPaths(vps []lockfile.ValuesPath) []lockfile.ValuesPath {
	if len(vps) == 0 {
		return vps
	}
	rank := func(a string) int {
		switch a {
		case lockfile.AccuracyManual:
			return 0
		case lockfile.AccuracySuffix:
			return 1
		default:
			return 2
		}
	}
	best := map[string]lockfile.ValuesPath{}
	for _, vp := range vps {
		if cur, ok := best[vp.Path]; !ok || rank(vp.Accuracy) < rank(cur.Accuracy) {
			best[vp.Path] = vp
		}
	}
	out := make([]lockfile.ValuesPath, 0, len(best))
	for _, vp := range best {
		out = append(out, vp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// suffixExtends reports whether image canonical repo ir is a hyphen-
// suffix extension of candidate canonical repo cr (cilium/operator →
// cilium/operator-generic). Registry + every parent path segment must be
// identical; only the final segment may be hyphen-extended.
func suffixExtends(cr, ir string) bool {
	if cr == "" || ir == "" || cr == ir {
		return false
	}
	cs := strings.Split(cr, "/")
	is := strings.Split(ir, "/")
	if len(cs) != len(is) {
		return false
	}
	for i := 0; i < len(cs)-1; i++ {
		if cs[i] != is[i] {
			return false
		}
	}
	cf, ifn := cs[len(cs)-1], is[len(is)-1]
	pre := cf + "-"
	return strings.HasPrefix(ifn, pre) && len(ifn) > len(pre)
}

// canonicalRepo returns the registry-qualified repository path for a ref,
// stripping any tag/digest and normalizing Docker Hub's implicit prefixes
// (`nginx` → `index.docker.io/library/nginx`).
func canonicalRepo(s string) string {
	if r, err := name.ParseReference(s); err == nil {
		return r.Context().Name()
	}
	if r, err := name.NewRepository(s); err == nil {
		return r.Name()
	}
	return strings.ToLower(s)
}
