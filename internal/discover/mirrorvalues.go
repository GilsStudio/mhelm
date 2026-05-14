package discover

import (
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
)

// buildMirrorValues produces a sparse values override that points each
// matched values path at the downstream mirror prefix. Designed to be
// passed to `helm install --values mirror-values.yaml` after the chart
// has been mirrored.
//
// Rewrite rules (auto-discovered, object form):
//   - With `registry` key:  registry → mirrorPrefix, repository → orig-reg/orig-repo
//   - Without `registry`:   repository → mirrorPrefix/orig-repo
//
// Auto-discovered string form: value → mirrorPrefix/orig-value
//
// extraImages: respects the user-supplied valuesPath. The destination
// shape is inferred from what's at that path in the chart's merged values
// (string in defaults → write string; map in defaults → write map).
//
// downstreamURL is chart.json#downstream.url; the oci:// prefix is stripped.
func buildMirrorValues(
	matches map[string][]imageCandidate,
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

func applyAutoRewrite(target map[string]any, c imageCandidate, mirrorPrefix string) {
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

// applyManualRewrite handles an extraImages entry. The destination shape
// is inferred from what's at the same path in the chart's merged values:
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
