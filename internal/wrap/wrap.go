// Package wrap authors a wrapper Helm chart that depends on a
// previously-mirrored upstream, bakes in digest-pinned image rewrites
// derived from chart-lock.json, bundles any extra manifests, packages
// the result, and pushes it to the downstream OCI registry.
//
// The wrapper is the user-facing artifact for an adopter who wants a
// single signed, locked, attested chart representing "the cluster's
// view of the upstream" — opposite path from the lightweight no-wrap
// adoption that uses image-values.yaml at install time.
package wrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/chartpull"
	"github.com/gilsstudio/mhelm/internal/discover"
	"github.com/gilsstudio/mhelm/internal/imagevalues"
	"github.com/gilsstudio/mhelm/internal/insecure"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/mirrorlayout"
	"github.com/google/go-containerregistry/pkg/name"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"sigs.k8s.io/yaml"
)

// canonicalRepo returns the registry-qualified repository path for a
// ref, stripping tag/digest and normalising docker-hub implicit prefixes.
// Duplicated in discover + verify for now; centralisation is a small
// future cleanup.
func canonicalRepo(s string) string {
	if r, err := name.ParseReference(s); err == nil {
		return r.Context().Name()
	}
	if r, err := name.NewRepository(s); err == nil {
		return r.Name()
	}
	return strings.ToLower(s)
}

// Result captures what `mhelm wrap` produced. cmd/wrap.go reads this
// to populate lockfile.Wrap.
type Result struct {
	ChartName                string
	ChartVersion             string
	ChartContentDigest       string // sha256:... of the wrapper .tgz bytes
	DownstreamRef            string // <prefix>/platform/<chart>:<version>
	DownstreamManifestDigest string // sha256:... after push

	// DependsOnRef is the mirrored upstream the wrapper depends on,
	// expressed as <prefix>/charts/<chart-name>:<chart-version>.
	DependsOnRef            string
	DependsOnManifestDigest string

	// DeployedImages is the deduped, canonical repo paths the wrapper
	// would actually deploy to the cluster (rendered against wrap's
	// valuesFiles). Audit trail for "what does this wrapper put into
	// the cluster?".
	DeployedImages []string
}

// MissingImagesError is returned by Run when the wrap fail-safe check
// finds images the wrapper would deploy that aren't in the lockfile's
// mirrored set. Each entry is a canonical repository path.
type MissingImagesError struct {
	Missing []string
}

func (e *MissingImagesError) Error() string {
	return fmt.Sprintf(
		"wrap fail-safe: %d image(s) would be deployed by the wrapper but are NOT in the mirror — add to mirror.discoveryValues or mirror.extraImages and re-mirror:\n  - %s",
		len(e.Missing), strings.Join(e.Missing, "\n  - "),
	)
}

// Run executes the wrap pipeline against the chart described by cf
// and the mirrored state recorded in lf. baseDir is the chart's
// directory (used to resolve wrap.valuesFiles + wrap.extraManifests).
func Run(ctx context.Context, cf chartfile.File, lf lockfile.File, baseDir string) (Result, error) {
	var res Result

	if cf.Wrap == nil {
		return res, fmt.Errorf("wrap.Run called without a wrap section — caller must check cf.Wrap != nil")
	}
	if lf.Mirror.Downstream.Ref == "" || len(lf.Mirror.Images) == 0 {
		return res, fmt.Errorf("wrap requires `mhelm mirror` to have run — chart-lock.json carries no downstream chart or images yet")
	}

	// 1. Load wrap.valuesFiles into a single user-overlay map.
	userOverlay, err := mergeValuesFiles(cf.Wrap.ValuesFiles, baseDir)
	if err != nil {
		return res, fmt.Errorf("load wrap.valuesFiles: %w", err)
	}

	// 2. Fail-safe: render the upstream chart with the user overlay
	// and confirm every rendered image is in the mirror.
	upstream, err := chartpull.Pull(ctx, cf.Mirror.Upstream)
	if err != nil {
		return res, fmt.Errorf("pull upstream: %w", err)
	}
	upstreamChart, err := loader.LoadArchive(bytes.NewReader(upstream.Bytes))
	if err != nil {
		return res, fmt.Errorf("load upstream chart: %w", err)
	}
	deployed, merged, err := discover.FindRenderedImages(upstreamChart, cf.Wrap.ValuesFiles, baseDir)
	if err != nil {
		return res, fmt.Errorf("render upstream with wrap.valuesFiles: %w", err)
	}
	mirrorSet := canonicalRepoSet(lf.Mirror.Images)
	var missing []string
	for _, repo := range deployed {
		if !mirrorSet[repo] {
			missing = append(missing, repo)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return res, &MissingImagesError{Missing: missing}
	}

	// 3. Build digest-pinned rewrites from lockfile images that have
	// known values-paths + downstream digests. Then layer user overlay
	// on top so the user always wins.
	rewrites := imagevalues.BuildDigestPinned(lf.Mirror.Images, merged)
	composed := chartutil.CoalesceTables(userOverlay, rewrites)

	// The wrapper reuses the mirrored chart's name — it lives in its own
	// platform/ registry namespace, so <prefix>/charts/<name> (faithful)
	// and <prefix>/platform/<name> (hardened) coexist without collision.
	// wrap.version defaults to the mirrored chart's version; an explicit
	// value lets the wrapper re-release independently of an upstream bump
	// while keeping every tag immutable.
	wrapName := lf.Mirror.Chart.Name
	wrapVersion := cf.Wrap.Version
	if wrapVersion == "" {
		wrapVersion = lf.Mirror.Chart.Version
	}

	// 4. Nest the composed values under the dependency's name so they
	// cascade to the subchart per Helm's subchart-values convention.
	depName := lf.Mirror.Chart.Name
	wrapperValues := map[string]any{}
	if len(composed) > 0 {
		wrapperValues[depName] = composed
	}

	// 5. Pre-fetch the mirrored dependency into the wrapper's
	// charts/ — Helm doesn't auto-resolve oci:// deps at render/install
	// time; the .tgz must live inside the wrapper.
	downstreamEp := chartfile.Endpoint{
		Type:    chartfile.TypeOCI,
		URL:     "oci://" + mirrorlayout.ChartRepo(cf.Mirror.Downstream.URL, depName),
		Version: lf.Mirror.Chart.Version,
	}
	depPull, err := chartpull.Pull(ctx, downstreamEp)
	if err != nil {
		return res, fmt.Errorf("fetch mirrored dependency %s@%s from %s: %w",
			depName, lf.Mirror.Chart.Version, downstreamEp.URL, err)
	}

	// 6. Author the wrapper *chart.Chart in memory.
	//
	// Subtle: chartutil.Save reads values.yaml bytes from chart.Raw[],
	// not from chart.Values. We marshal here and add to Raw so the
	// packaged .tgz carries our rewrites; setting Values too keeps
	// in-memory access symmetric for callers that don't round-trip.
	valuesYAML, err := yaml.Marshal(wrapperValues)
	if err != nil {
		return res, fmt.Errorf("marshal wrapper values: %w", err)
	}
	wrapper := &chart.Chart{
		Metadata: &chart.Metadata{
			APIVersion: chart.APIVersionV2,
			Name:       wrapName,
			Version:    wrapVersion,
			Type:       "application",
			Dependencies: []*chart.Dependency{{
				Name:    depName,
				Version: lf.Mirror.Chart.Version,
				// Informational only — the dep .tgz is vendored into the
				// wrapper's charts/ below, so Helm never resolves this at
				// install time. Point it at the real charts/ namespace.
				Repository: "oci://" + mirrorlayout.ChartsBase(cf.Mirror.Downstream.URL),
			}},
		},
		Values: wrapperValues,
		Raw: []*chart.File{{
			Name: chartutil.ValuesfileName,
			Data: valuesYAML,
		}},
		Files: []*chart.File{{
			// Place the mirrored dep .tgz under charts/. chartutil.Save
			// writes everything in chart.Files to the corresponding
			// path in the packaged .tgz, so this lands at the right
			// location for Helm's subchart resolution.
			Name: fmt.Sprintf("charts/%s-%s.tgz", depName, lf.Mirror.Chart.Version),
			Data: depPull.Bytes,
		}},
	}

	// extraManifests → copied verbatim into templates/. Helm templates
	// will run on these at install time as on any other manifest;
	// mhelm itself does not interpolate.
	for _, m := range cf.Wrap.ExtraManifests {
		p := m
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return res, fmt.Errorf("read extraManifest %s: %w", p, err)
		}
		wrapper.Templates = append(wrapper.Templates, &chart.File{
			Name: "templates/" + filepath.Base(m),
			Data: b,
		})
	}

	// 7. Package via chartutil.Save (writes a .tgz to a tmp dir).
	tmpDir, err := os.MkdirTemp("", "mhelm-wrap-")
	if err != nil {
		return res, fmt.Errorf("tmp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tgzPath, err := chartutil.Save(wrapper, tmpDir)
	if err != nil {
		return res, fmt.Errorf("chartutil.Save: %w", err)
	}
	tgz, err := os.ReadFile(tgzPath)
	if err != nil {
		return res, fmt.Errorf("read packaged wrapper: %w", err)
	}

	// 8. Push wrapper to downstream OCI.
	settings := cli.New()
	opts := []registry.ClientOption{registry.ClientOptCredentialsFile(settings.RegistryConfig)}
	if insecure.Enabled() {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}
	client, err := registry.NewClient(opts...)
	if err != nil {
		return res, fmt.Errorf("registry client: %w", err)
	}
	destRef := fmt.Sprintf("%s:%s",
		mirrorlayout.PlatformRepo(cf.Mirror.Downstream.URL, wrapName),
		wrapVersion,
	)
	pushRes, err := client.Push(tgz, destRef)
	if err != nil {
		return res, fmt.Errorf("push wrapper %s: %w", destRef, err)
	}

	res = Result{
		ChartName:               wrapName,
		ChartVersion:            wrapVersion,
		ChartContentDigest:      lockfile.ContentDigest(tgz),
		DownstreamRef:           destRef,
		DependsOnRef:            lf.Mirror.Downstream.Ref,
		DependsOnManifestDigest: lf.Mirror.Downstream.OCIManifestDigest,
		DeployedImages:          deployed,
	}
	if pushRes != nil && pushRes.Manifest != nil {
		res.DownstreamManifestDigest = pushRes.Manifest.Digest
	}
	return res, nil
}

// mergeValuesFiles loads each path (resolved relative to baseDir) and
// coalesces them in order. Matches the helper used by discover.
func mergeValuesFiles(paths []string, baseDir string) (map[string]any, error) {
	out := map[string]any{}
	for _, vf := range paths {
		p := vf
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var v map[string]any
		if err := yaml.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		out = chartutil.CoalesceTables(out, v)
	}
	return out, nil
}

// canonicalRepoSet builds a lookup set keyed by canonical repository
// path so we can check each rendered image against the mirrored
// inventory regardless of tag/digest suffix.
func canonicalRepoSet(images []lockfile.Image) map[string]bool {
	out := make(map[string]bool, len(images))
	for _, img := range images {
		out[canonicalRepo(img.Ref)] = true
	}
	return out
}

// ToLockfileBlock converts a Result into the lockfile shape so callers
// (cmd/wrap.go) can persist it without poking at internals.
func (r Result) ToLockfileBlock(version string, at time.Time) lockfile.WrapBlock {
	return lockfile.WrapBlock{
		Chart: lockfile.WrapChart{
			Name:               r.ChartName,
			Version:            r.ChartVersion,
			Ref:                r.DownstreamRef,
			OCIManifestDigest:  r.DownstreamManifestDigest,
			ChartContentDigest: r.ChartContentDigest,
		},
		DependsOn: lockfile.WrapDep{
			Ref:               r.DependsOnRef,
			OCIManifestDigest: r.DependsOnManifestDigest,
		},
		DeployedImages: r.DeployedImages,
		Tool:           "mhelm wrap",
		Version:        version,
		Timestamp:      at,
	}
}
