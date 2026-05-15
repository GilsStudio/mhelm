// Package mirrorlayout is the single source of truth for how mhelm
// namespaces artifacts under a downstream OCI registry.
//
// Three artifact classes, three namespaces beneath chart.json#
// mirror.downstream.url:
//
//   - charts/<name>      faithful byte-copy of the upstream chart
//                        (`mhelm mirror`)
//   - platform/<name>    the hardened, digest-pinned wrapper chart you
//                        actually deploy (`mhelm wrap`)
//   - images/<src-path>  every mirrored container image, upstream path
//                        preserved so value rewrites stay mechanical
//
// The push side and the value-rewrite side MUST agree on these paths or
// adopters pull broken refs. They used to agree only by coincidence
// (each rebuilt the prefix by hand). Routing every site through this
// package makes divergence structurally impossible: change a path here
// and both sides move together.
package mirrorlayout

import "strings"

// Prefix is chart.json#mirror.downstream.url with the oci:// scheme and
// any trailing slash removed — the bare registry+path every artifact is
// namespaced beneath (e.g. "ghcr.io/myorg/mirror").
func Prefix(downstreamURL string) string {
	p := strings.TrimPrefix(downstreamURL, "oci://")
	return strings.TrimSuffix(p, "/")
}

// ChartsBase is the namespace the faithful upstream-chart copies live
// under: <prefix>/charts. Used as a Helm OCI dependency repository
// (Helm appends the chart name itself).
func ChartsBase(downstreamURL string) string {
	return Prefix(downstreamURL) + "/charts"
}

// PlatformBase is the namespace the hardened wrapper charts live under:
// <prefix>/platform.
func PlatformBase(downstreamURL string) string {
	return Prefix(downstreamURL) + "/platform"
}

// ImagePrefix is the namespace every mirrored container image is nested
// beneath: <prefix>/images. Callers append the preserved upstream path
// (registry host included), so it can never collide with charts/ or
// platform/. This is the value both the image push (imagemirror) and
// the value rewrite (imagevalues) MUST share.
func ImagePrefix(downstreamURL string) string {
	return Prefix(downstreamURL) + "/images"
}

// ChartRepo is the full repository path (no tag) for one mirrored
// upstream chart: <prefix>/charts/<name>.
func ChartRepo(downstreamURL, chartName string) string {
	return ChartsBase(downstreamURL) + "/" + chartName
}

// PlatformRepo is the full repository path (no tag) for one wrapper
// chart: <prefix>/platform/<name>.
func PlatformRepo(downstreamURL, chartName string) string {
	return PlatformBase(downstreamURL) + "/" + chartName
}
