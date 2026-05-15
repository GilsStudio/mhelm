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

// BuildTagBased produces a sparse override that points each resolved
// image's values path(s) at the downstream mirror prefix. Tag is
// preserved from the chart's value; the registry is rewritten and the
// resolved manifest digest (when known) is pinned. Designed to be passed
// to `helm install --values image-values.yaml` after mirroring.
//
// Driven by the deduped, digest-resolved lockfile images (each carrying
// its matched ValuesPaths) so the overlay can always emit the resolved
// digest — not only when the chart defaults already carry one. extras is
// consulted only for mirror.extraImages[].overridePath, which emits a
// single pinned ref string alongside the structured form (the escape
// hatch charts like cilium expose to bypass per-cloud suffix concat).
//
// downstreamURL is chart.json#mirror.downstream.url; the oci:// prefix
// is stripped.
func BuildTagBased(
	images []lockfile.Image,
	extras []chartfile.ExtraImage,
	merged map[string]any,
	downstreamURL string,
) map[string]any {
	prefix := strings.TrimPrefix(downstreamURL, "oci://")
	out := map[string]any{}

	for _, img := range images {
		for _, vp := range img.ValuesPaths {
			applyTagRewrite(out, vp.Path, img.Ref, img.Digest, prefix, merged)
		}
	}

	// overridePath: emit the whole pinned ref as a single string so a
	// chart's `image.override`-style field bypasses suffix concatenation.
	overrideByKey := map[string]string{}
	for _, e := range extras {
		if e.OverridePath != "" {
			overrideByKey[mergeKey(e.Ref)] = e.OverridePath
		}
	}
	if len(overrideByKey) > 0 {
		for _, img := range images {
			if op, ok := overrideByKey[mergeKey(img.Ref)]; ok {
				applyOverrideRewrite(out, op, img.Ref, img.Digest, prefix)
			}
		}
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

// applyTagRewrite rewrites one values path to point at the mirror,
// preserving the chart's shape (string vs map). The shape is inferred
// from the chart's merged values at that path:
//   - map with `repository` → map override (registry/repository/tag),
//     plus the resolved digest (and any per-cloud <x>Digest field the
//     chart defaults carry) — emitted even when the chart default has no
//     `digest` key, so consumers like cilium that read their own digest
//     field still benefit.
//   - string or absent → string override (mirrorPrefix/<chart-string>),
//     falling back to mirrorPrefix/<ref> when the path is absent.
//
// ref is the resolved lockfile image ref; digest its resolved manifest
// digest (may be empty).
func applyTagRewrite(target map[string]any, path, ref, digest, mirrorPrefix string, merged map[string]any) {
	parent, leaf := navigateOrCreate(target, path)

	srcShape := lookupPath(merged, path)
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
			emitDigests(rewritten, srcMap, repo, ref, digest)
			parent[leaf] = rewritten
			return
		}
	}
	if s := toString(srcShape); s != "" {
		// Preserve the chart's literal string (keeps the upstream tag).
		parent[leaf] = mirrorPrefix + "/" + s
		return
	}
	parent[leaf] = mirrorPrefix + "/" + ref
}

// applyOverrideRewrite writes a single pinned string ref at overridePath:
// <mirrorPrefix>/<repo>[:tag][@digest]. Always a string — this is what a
// chart's `image.override` field expects (it bypasses the chart's own
// per-cloud suffix concatenation).
func applyOverrideRewrite(target map[string]any, overridePath, ref, digest, mirrorPrefix string) {
	parent, leaf := navigateOrCreate(target, overridePath)
	bare := ref
	if i := strings.Index(bare, "@"); i >= 0 {
		bare = bare[:i]
	}
	s := mirrorPrefix + "/" + bare
	if digest != "" {
		s += "@" + digest
	}
	parent[leaf] = s
}

// emitDigests sets `digest` to the resolved manifest digest (when known)
// and, when the chart's defaults at this path use per-cloud `<x>Digest`
// fields instead of a bare `digest`, sets the one whose prefix matches
// the image's repo suffix (cilium's operator → operator-generic ⇒
// genericDigest). With a single per-cloud field it is set regardless of
// suffix; with several and no suffix match, all are set to the same
// resolved digest (same image, same bytes, same digest). Per-cloud
// fields that don't match are intentionally not copied — the chart's
// own defaults still cover the unused clouds.
func emitDigests(rewritten, srcMap map[string]any, defaultRepo, imageRef, digest string) {
	if d, ok := srcMap["digest"]; ok && digest == "" {
		rewritten["digest"] = d // preserve chart default when nothing resolved
		return
	}
	if digest == "" {
		return
	}
	rewritten["digest"] = digest

	var perCloud []string
	for k := range srcMap {
		if k != "digest" && strings.HasSuffix(k, "Digest") && isLowerAlnum(strings.TrimSuffix(k, "Digest")) {
			perCloud = append(perCloud, k)
		}
	}
	if len(perCloud) == 0 {
		return
	}
	suffix := repoSuffix(defaultRepo, canonicalRepoOf(imageRef))
	switch {
	case suffix != "":
		for _, k := range perCloud {
			if strings.EqualFold(strings.TrimSuffix(k, "Digest"), suffix) {
				rewritten[k] = digest
			}
		}
	case len(perCloud) == 1:
		rewritten[perCloud[0]] = digest
	default:
		for _, k := range perCloud {
			rewritten[k] = digest
		}
	}
}

// repoSuffix returns the hyphen-suffix token by which imageRepo extends
// defaultRepo on the final path segment (cilium/operator,
// cilium/operator-generic → "generic"). Empty when imageRepo is not such
// an extension (incl. when they are equal).
func repoSuffix(defaultRepo, imageRepo string) string {
	df := finalSegment(stripRefSuffix(defaultRepo))
	imf := finalSegment(imageRepo)
	if df == "" || imf == "" || imf == df {
		return ""
	}
	if pre := df + "-"; strings.HasPrefix(imf, pre) && len(imf) > len(pre) {
		return imf[len(pre):]
	}
	return ""
}

func finalSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func canonicalRepoOf(ref string) string {
	bare := stripRefSuffix(ref)
	if i := strings.Index(ref, "@"); i >= 0 && i < len(bare) {
		bare = stripRefSuffix(ref[:i])
	}
	return bare
}

func isLowerAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// mergeKey is repo[:tag] with any @sha256 digest stripped — refs that
// differ only by an appended digest collapse to the same key. Kept here
// (mirrors internal/discover.mergeKey) so the override lookup matches the
// same identity the lockfile deduped on without an import cycle.
func mergeKey(ref string) string {
	bare := ref
	if i := strings.Index(bare, "@"); i >= 0 {
		bare = bare[:i]
	}
	return strings.ToLower(bare)
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
			emitDigests(rewritten, srcMap, toString(srcMap["repository"]), downstreamRef, digest)
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
