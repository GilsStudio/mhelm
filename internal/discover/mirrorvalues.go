package discover

import "strings"

// buildMirrorValues produces a sparse values override that points each
// matched values path at the downstream mirror prefix. Designed to be
// passed to `helm install --values mirror-values.yaml` after the chart
// has been mirrored.
//
// Rewrite rules:
//   - Object form with `registry`: registry → mirrorPrefix, repository → orig-reg/orig-repo
//   - Object form without `registry`: repository → mirrorPrefix/orig-repo
//   - String form: value → mirrorPrefix/orig-value
//
// downstreamURL is chart.json#downstream.url; the oci:// prefix is stripped.
func buildMirrorValues(matches map[string][]imageCandidate, downstreamURL string) map[string]any {
	prefix := strings.TrimPrefix(downstreamURL, "oci://")
	out := map[string]any{}

	for _, candidates := range matches {
		for _, c := range candidates {
			applyAutoRewrite(out, c, prefix)
		}
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
