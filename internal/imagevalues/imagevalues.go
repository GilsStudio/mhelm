// Package imagevalues builds sparse `helm install --values` overrides
// that rewrite a chart's image refs to point at a downstream mirror.
//
// Two output shapes share the same logic:
//
//   - BuildTagBased — produced by `mhelm discover` as `image-values.yaml`
//     for adopters who install the upstream chart directly with their
//     mirror as an image source. Tag/digest preserved from the upstream
//     value; only registry is rewritten.
//   - BuildDigestPinned — produced by `mhelm wrap` and baked into the
//     wrapper chart's values.yaml. Every image is pinned to the
//     mirrored OCI manifest digest captured in chart-lock.json.
//
// Shape (string vs map with registry/repository/tag) is preserved from
// the chart's merged values so the chart's templates still consume what
// they expect.
package imagevalues

import (
	"fmt"
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

// Candidate is one (values-path → image-ref) tuple discovered by mhelm
// discover. StringForm/ObjectForm carry the shape the chart's defaults
// use at this path so rewriting can preserve that shape.
type Candidate struct {
	Path string
	Ref  string

	// Exactly one of these is set:
	StringForm string                 // for `image: "<ref>"` form
	ObjectForm map[string]interface{} // for `image: {registry, repository, tag, digest}` form
}

// BuildTagBased produces a sparse override that points each matched
// values path at the downstream mirror prefix. Tag/digest preserved
// from the upstream value; only the registry is rewritten. Designed to
// be passed to `helm install --values image-values.yaml` after the
// chart has been mirrored.
//
// downstreamURL is chart.json#mirror.downstream.url; the oci:// prefix
// is stripped.
func BuildTagBased(
	matches map[string][]Candidate,
	extras []chartfile.ExtraImage,
	merged map[string]any,
	downstreamURL string,
) map[string]any {
	prefix := strings.TrimPrefix(downstreamURL, "oci://")
	out := map[string]any{}

	for _, candidates := range matches {
		for _, c := range candidates {
			applyAutoRewrite(out, c, prefix)
		}
	}

	for _, e := range extras {
		if e.ValuesPath == "" {
			continue
		}
		applyManualRewrite(out, e, prefix, merged)
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// BuildDigestPinned produces a sparse override that pins every image
// in the lockfile (with a known DownstreamRef + DownstreamDigest +
// ValuesPaths) to its mirrored digest. Used by `mhelm wrap` to bake
// supply-chain-strong references into the wrapper's values.yaml.
//
// Shape at each path is inferred from merged (the chart's rendered
// values): map with `repository` key → map form, otherwise string form.
func BuildDigestPinned(images []lockfile.Image, merged map[string]any) map[string]any {
	out := map[string]any{}
	for _, img := range images {
		if img.DownstreamRef == "" || img.DownstreamDigest == "" {
			continue
		}
		for _, vp := range img.ValuesPaths {
			applyDigestRewrite(out, vp.Path, img.DownstreamRef, img.DownstreamDigest, merged)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyAutoRewrite(target map[string]any, c Candidate, mirrorPrefix string) {
	parent, leaf := navigateOrCreate(target, c.Path)

	if c.StringForm != "" {
		parent[leaf] = mirrorPrefix + "/" + c.StringForm
		return
	}

	src := c.ObjectForm
	repo := toString(src["repository"])
	reg := toString(src["registry"])

	rewritten := map[string]any{}
	if reg != "" {
		rewritten["registry"] = mirrorPrefix
		rewritten["repository"] = reg + "/" + repo
	} else {
		rewritten["repository"] = mirrorPrefix + "/" + repo
	}
	if tag, ok := src["tag"]; ok {
		rewritten["tag"] = tag
	}
	if d, ok := src["digest"]; ok {
		rewritten["digest"] = d
	}
	parent[leaf] = rewritten
}

// applyManualRewrite handles a chart.json#mirror.extraImages entry.
// The destination shape is inferred from what's at the same path in
// the chart's merged values:
//   - map with `repository` key → map override (same rewrite rules as auto)
//   - string or missing → string override (mirrorPrefix/orig-ref)
func applyManualRewrite(target map[string]any, e chartfile.ExtraImage, mirrorPrefix string, merged map[string]any) {
	parent, leaf := navigateOrCreate(target, e.ValuesPath)

	srcShape := lookupPath(merged, e.ValuesPath)
	if srcMap, ok := srcShape.(map[string]any); ok {
		if repo := toString(srcMap["repository"]); repo != "" {
			reg := toString(srcMap["registry"])
			rewritten := map[string]any{}
			if reg != "" {
				rewritten["registry"] = mirrorPrefix
				rewritten["repository"] = reg + "/" + repo
			} else {
				rewritten["repository"] = mirrorPrefix + "/" + repo
			}
			if tag, ok := srcMap["tag"]; ok {
				rewritten["tag"] = tag
			}
			if d, ok := srcMap["digest"]; ok {
				rewritten["digest"] = d
			}
			parent[leaf] = rewritten
			return
		}
	}
	parent[leaf] = mirrorPrefix + "/" + e.Ref
}

// applyDigestRewrite writes a digest-pinned rewrite at one values
// path. downstreamRef is the bare mirrored ref (no tag/digest suffix
// — registry/repo only is preferred for cleanliness, but a tagged ref
// is also accepted and the tag will be stripped). digest is the
// sha256:... manifest digest.
//
// Shape is inferred from merged: map form when defaults are a map
// with `repository`, string form otherwise. String form combines
// registry+repo into a single repository path:
// `<mirror>/<repo>@sha256:...`. Map form decomposes when possible.
func applyDigestRewrite(target map[string]any, path, downstreamRef, digest string, merged map[string]any) {
	parent, leaf := navigateOrCreate(target, path)
	reg, repo := splitRegistryRepo(downstreamRef)

	if srcMap, ok := lookupPath(merged, path).(map[string]any); ok {
		if _, hasRepo := srcMap["repository"]; hasRepo {
			rewritten := map[string]any{}
			if _, hasRegistry := srcMap["registry"]; hasRegistry && reg != "" {
				rewritten["registry"] = reg
				rewritten["repository"] = repo
			} else if reg != "" {
				rewritten["repository"] = reg + "/" + repo
			} else {
				rewritten["repository"] = repo
			}
			rewritten["digest"] = digest
			parent[leaf] = rewritten
			return
		}
	}
	// String form (or path absent in merged): single ref pinned to digest.
	full := repo
	if reg != "" {
		full = reg + "/" + repo
	}
	parent[leaf] = fmt.Sprintf("%s@%s", full, digest)
}

// splitRegistryRepo splits a bare ref like
// `ghcr.io/myorg/mirror/cilium/cilium` into (registry, repository).
// A ref carrying `:tag` or `@digest` has those stripped first. The
// registry is the segment before the first `/`; everything else is the
// repository path.
func splitRegistryRepo(ref string) (registry, repository string) {
	bare := stripRefSuffix(ref)
	i := strings.Index(bare, "/")
	if i < 0 {
		return "", bare
	}
	return bare[:i], bare[i+1:]
}

func stripRefSuffix(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		// Avoid treating a port (e.g. `localhost:5000/foo`) as a tag.
		if !strings.Contains(ref[i+1:], "/") {
			ref = ref[:i]
		}
	}
	return ref
}

func navigateOrCreate(target map[string]any, path string) (parent map[string]any, leaf string) {
	parts := strings.Split(path, ".")
	cur := target
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	return cur, parts[len(parts)-1]
}

func lookupPath(root map[string]any, path string) any {
	parts := strings.Split(path, ".")
	var cur any = root
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
		if cur == nil {
			return nil
		}
	}
	return cur
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
