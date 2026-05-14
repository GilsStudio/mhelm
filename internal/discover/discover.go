// Package discover renders a Helm chart with the consumer's intended values
// and extracts every container image reference it carries. The container
// walker handles the common case; env-var, ConfigMap-data, and CRD-spec
// extractors catch operator patterns; chart.json#extraImages is the
// always-available manual escape hatch.
//
// Untrusted (regex-derived) candidates are confirmed by HEADing the
// registry via crane.Digest — strings that aren't real pullable images
// are dropped.
package discover

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/chartpull"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"sigs.k8s.io/yaml"
)

type Result struct {
	ChartName              string
	ChartVersion           string
	ChartContentDigest     string
	UpstreamManifestDigest string
	Images                 []lockfile.Image
	// MirrorValues is a sparse override map ready to be marshaled to
	// mirror-values.yaml. nil if no values paths could be matched.
	MirrorValues map[string]any
}

// Run pulls the chart referenced by cf.Mirror.Upstream, renders it with
// the effective discovery values (cf.Mirror.DiscoveryValues, falling
// back to cf.Wrap.ValuesFiles for v0.2.0 bridging), discovers every
// container image it references (manifest, env, ConfigMap, CRD-spec,
// manual), resolves their registry digests, and builds the
// mirror-values override.
func Run(ctx context.Context, cf chartfile.File, baseDir string) (Result, error) {
	var res Result

	pulled, err := chartpull.Pull(ctx, cf.Mirror.Upstream)
	if err != nil {
		return res, fmt.Errorf("pull: %w", err)
	}
	res.ChartContentDigest = lockfile.ContentDigest(pulled.Bytes)
	res.UpstreamManifestDigest = pulled.OCIManifestDigest

	c, err := loader.LoadArchive(bytes.NewReader(pulled.Bytes))
	if err != nil {
		return res, fmt.Errorf("load chart: %w", err)
	}
	if c.Metadata == nil {
		return res, fmt.Errorf("chart has no metadata")
	}
	res.ChartName = c.Metadata.Name
	res.ChartVersion = c.Metadata.Version

	valuesFiles := cf.DiscoveryValuesEffective()
	rendered, merged, err := renderChart(c, valuesFiles, baseDir)
	if err != nil {
		return res, fmt.Errorf("render: %w", err)
	}

	// Images discovered from the rendered manifests are tagged
	// "discoveryValues" when any values file influenced the render, else
	// "defaults". Annotation entries (publisher-declared) and manual
	// extras carry their own DiscoveredVia values.
	renderedVia := lockfile.DiscoveredViaDefaults
	if len(valuesFiles) > 0 {
		renderedVia = lockfile.DiscoveredViaDiscoveryValues
	}

	// 1. Run every extractor against every rendered doc.
	docs := parseDocs(rendered)
	var cands []candidate
	for _, doc := range docs {
		cands = append(cands, extractFromContainers(doc)...)
		cands = append(cands, extractFromEnv(doc)...)
		cands = append(cands, extractFromConfigMap(doc)...)
		if !isBuiltinKind(doc) {
			cands = append(cands, extractFromCRDSpec(doc)...)
		}
	}
	for i := range cands {
		cands[i].DiscoveredVia = renderedVia
	}

	// 2. Chart.yaml annotation entries — trusted (publisher-declared).
	for _, ref := range extractFromAnnotations(c) {
		cands = append(cands, candidate{
			Ref:           ref,
			Source:        lockfile.SourceAnnotation,
			DiscoveredVia: lockfile.DiscoveredViaDefaults,
			Trusted:       true,
		})
	}

	// 3. Manual extraImages from chart.json — trusted (user-declared).
	for _, e := range cf.Mirror.ExtraImages {
		cands = append(cands, candidate{
			Ref:           e.Ref,
			Source:        lockfile.SourceManual,
			DiscoveredVia: lockfile.DiscoveredViaExtraImages,
			Trusted:       true,
		})
	}

	// 4. Validate, dedupe, label sources.
	res.Images = validateAndDedupe(cands)

	// 5. Match each image to values paths in the chart's merged values, and
	// build the sparse mirror-values override.
	imageRefs := make([]string, 0, len(res.Images))
	for _, img := range res.Images {
		imageRefs = append(imageRefs, img.Ref)
	}
	ivc := findImageCandidates(merged)
	matches := matchCandidates(imageRefs, ivc)
	for i, img := range res.Images {
		for _, mc := range matches[img.Ref] {
			res.Images[i].ValuesPaths = append(res.Images[i].ValuesPaths, lockfile.ValuesPath{
				Path:     mc.Path,
				Accuracy: lockfile.AccuracyHeuristic,
			})
		}
	}
	// User-supplied valuesPath from extraImages overrides/augments the
	// heuristic match (the user explicitly told us where this image lives).
	for _, e := range cf.Mirror.ExtraImages {
		if e.ValuesPath == "" {
			continue
		}
		for i := range res.Images {
			if res.Images[i].Ref != e.Ref {
				continue
			}
			res.Images[i].ValuesPaths = append(res.Images[i].ValuesPaths, lockfile.ValuesPath{
				Path:     e.ValuesPath,
				Accuracy: lockfile.AccuracyManual,
			})
		}
	}

	res.MirrorValues = buildMirrorValues(matches, cf.Mirror.ExtraImages, merged, cf.Mirror.Downstream.URL)
	return res, nil
}

// renderChart returns the rendered template output and the merged Values
// (chart defaults coalesced with any cf.ValuesFiles overrides). The merged
// map is what findImageCandidates walks.
func renderChart(c *chart.Chart, valuesFiles []string, baseDir string) (map[string]string, map[string]any, error) {
	overrides := map[string]any{}
	for _, vf := range valuesFiles {
		p := vf
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("read values %s: %w", p, err)
		}
		var v map[string]any
		if err := yaml.Unmarshal(b, &v); err != nil {
			return nil, nil, fmt.Errorf("parse values %s: %w", p, err)
		}
		overrides = chartutil.CoalesceTables(overrides, v)
	}

	relOpts := chartutil.ReleaseOptions{
		Name:      "mhelm-discover",
		Namespace: "default",
		Revision:  1,
		IsInstall: true,
	}
	renderValues, err := chartutil.ToRenderValues(c, overrides, relOpts, chartutil.DefaultCapabilities)
	if err != nil {
		return nil, nil, err
	}
	rendered, err := engine.Render(c, renderValues)
	if err != nil {
		return nil, nil, err
	}
	merged, _ := renderValues["Values"].(chartutil.Values)
	return rendered, merged, nil
}

// extractFromAnnotations reads Chart.yaml's `artifacthub.io/images`
// annotation (a YAML list of `{name, image}` entries) when present.
func extractFromAnnotations(c *chart.Chart) []string {
	if c.Metadata == nil || c.Metadata.Annotations == nil {
		return nil
	}
	raw, ok := c.Metadata.Annotations["artifacthub.io/images"]
	if !ok || raw == "" {
		return nil
	}
	var entries []struct {
		Image string `yaml:"image" json:"image"`
	}
	if err := yaml.Unmarshal([]byte(raw), &entries); err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Image != "" {
			out = append(out, e.Image)
		}
	}
	return out
}
