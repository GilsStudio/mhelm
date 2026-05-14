package discover

import (
	"fmt"
	"strings"

	"github.com/gilsstudio/mhelm/internal/imagevalues"
	"github.com/google/go-containerregistry/pkg/name"
)

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
func matchCandidates(images []string, candidates []imageCandidate) map[string][]imageCandidate {
	out := make(map[string][]imageCandidate, len(images))
	for _, img := range images {
		ic := canonicalRepo(img)
		for _, c := range candidates {
			if canonicalRepo(c.Ref) == ic {
				out[img] = append(out[img], c)
			}
		}
	}
	return out
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
