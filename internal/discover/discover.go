// Package discover renders a Helm chart with the consumer's intended values
// and extracts every container image reference from the rendered manifests,
// honoring Chart.yaml's artifacthub.io/images annotation when present.
//
// Each image's manifest digest is resolved against the registry via HEAD
// (crane.Digest) so the lockfile pins by sha256 rather than mutable tag.
package discover

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/chartpull"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/crane"
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

// Run pulls the chart referenced by cf.Upstream, renders it with the values
// listed in cf.ValuesFiles (resolved relative to baseDir), discovers all
// container image refs, and resolves their registry digests.
func Run(ctx context.Context, cf chartfile.File, baseDir string) (Result, error) {
	var res Result

	pulled, err := chartpull.Pull(ctx, cf.Upstream)
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

	rendered, merged, err := renderChart(c, cf.ValuesFiles, baseDir)
	if err != nil {
		return res, fmt.Errorf("render: %w", err)
	}

	refs := extractFromManifests(rendered)
	refs = append(refs, extractFromAnnotations(c)...)
	refs = dedupe(refs)
	sort.Strings(refs)

	candidates := findImageCandidates(merged)
	matches := matchCandidates(refs, candidates)

	for _, ref := range refs {
		img := lockfile.Image{Ref: ref}
		if d, err := crane.Digest(ref); err == nil {
			img.Digest = d
		}
		for _, c := range matches[ref] {
			img.ValuesPaths = append(img.ValuesPaths, lockfile.ValuesPath{
				Path:     c.Path,
				Accuracy: lockfile.AccuracyHeuristic,
			})
		}
		res.Images = append(res.Images, img)
	}

	res.MirrorValues = buildMirrorValues(matches, cf.Downstream.URL)
	return res, nil
}

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

// extractFromManifests walks every rendered K8s manifest and collects any
// `image:` string field whose ancestor is a `containers` or `initContainers`
// slice. Misses operator-managed images (documented limitation, addressed
// in a later phase with extended extractors).
func extractFromManifests(rendered map[string]string) []string {
	var images []string
	for _, content := range rendered {
		for _, doc := range strings.Split(content, "\n---") {
			doc = strings.TrimSpace(doc)
			if doc == "" {
				continue
			}
			var m map[string]any
			if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
				continue
			}
			walk(m, false, &images)
		}
	}
	return images
}

func walk(node any, underContainers bool, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			switch k {
			case "image":
				if underContainers {
					if s, ok := child.(string); ok && s != "" {
						*out = append(*out, s)
					}
				}
			case "containers", "initContainers":
				walk(child, true, out)
			default:
				walk(child, false, out)
			}
		}
	case []any:
		for _, item := range v {
			walk(item, underContainers, out)
		}
	}
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

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
