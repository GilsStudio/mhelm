// Package drift detects three classes of upstream/downstream divergence
// against a chart-lock.json:
//
//   - upstream-rotation: upstream now publishes different bytes under the
//     same pinned ref (immutable-tag violation = supply-chain incident).
//   - downstream-tampered: downstream registry digest no longer matches the
//     digest we mirrored (registry compromise or accidental overwrite).
//   - new-version-available: upstream has released a higher semver than
//     chart.json#upstream.version (operational signal, not an incident).
//
// Findings are returned as lockfile.Drift so callers can persist the
// audit trail to chart-lock.json.
package drift

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/insecure"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
)

const MaxParallel = 8

type Options struct {
	UpstreamRotation    bool
	DownstreamTampering bool
	NewVersions         bool
}

func DefaultOptions() Options {
	return Options{UpstreamRotation: true, DownstreamTampering: true, NewVersions: true}
}

// Run executes the enabled drift checks and returns the lockfile.Drift
// payload to persist. Per-check failures are reported as findings (e.g.
// "couldn't reach registry") rather than returned as errors, so a single
// flaky network call doesn't mask the rest of the results.
func Run(ctx context.Context, cf chartfile.File, lf lockfile.File, opts Options) lockfile.Drift {
	out := lockfile.Drift{CheckedAt: time.Now().UTC()}

	if opts.UpstreamRotation {
		out.Findings = append(out.Findings, checkChartUpstream(cf, lf)...)
		out.Findings = append(out.Findings, checkImagesUpstream(lf)...)
	}
	if opts.DownstreamTampering {
		out.Findings = append(out.Findings, checkChartDownstream(lf)...)
		out.Findings = append(out.Findings, checkImagesDownstream(lf)...)
	}
	if opts.NewVersions {
		out.Findings = append(out.Findings, checkNewVersions(cf, lf)...)
	}
	return out
}

// checkChartUpstream re-resolves the upstream chart at the pinned version
// and compares its content digest against the lockfile.
func checkChartUpstream(cf chartfile.File, lf lockfile.File) []lockfile.DriftFinding {
	switch cf.Mirror.Upstream.Type {
	case chartfile.TypeRepo:
		idx, err := loadIndex(cf.Mirror.Upstream.URL)
		if err != nil {
			return nil
		}
		return compareChartRepoDigest(idx, cf, lf)
	case chartfile.TypeOCI:
		if lf.Mirror.Upstream.OCIManifestDigest == "" {
			return nil
		}
		ref := strings.TrimPrefix(cf.Mirror.Upstream.URL, "oci://") + ":" + cf.Mirror.Upstream.Version
		got, err := crane.Digest(ref, craneOpts()...)
		if err != nil {
			return nil
		}
		return compareChartOCIDigest(ref, got, lf)
	}
	return nil
}

// compareChartRepoDigest compares a Helm repo index's reported digest for
// the pinned chart version against the lockfile. Pure (no network).
func compareChartRepoDigest(idx *repo.IndexFile, cf chartfile.File, lf lockfile.File) []lockfile.DriftFinding {
	cv, err := idx.Get(cf.Mirror.Upstream.Name, cf.Mirror.Upstream.Version)
	if err != nil {
		return []lockfile.DriftFinding{{
			Kind:    lockfile.DriftKindUpstreamMissing,
			Subject: cf.Mirror.Upstream.Name + "@" + cf.Mirror.Upstream.Version,
			Note:    "upstream index.yaml no longer lists this chart version",
		}}
	}
	expected := strings.TrimPrefix(lf.Mirror.Upstream.ChartContentDigest, "sha256:")
	if expected == "" || strings.EqualFold(cv.Digest, expected) {
		return nil
	}
	return []lockfile.DriftFinding{{
		Kind:     lockfile.DriftKindUpstreamRotation,
		Subject:  cf.Mirror.Upstream.Name + "@" + cf.Mirror.Upstream.Version,
		Expected: lf.Mirror.Upstream.ChartContentDigest,
		Actual:   "sha256:" + cv.Digest,
		Note:     "upstream index.yaml now reports a different digest for this chart version",
	}}
}

// compareChartOCIDigest compares an OCI manifest digest against the lockfile.
// Pure (no network).
func compareChartOCIDigest(ref, got string, lf lockfile.File) []lockfile.DriftFinding {
	if got == lf.Mirror.Upstream.OCIManifestDigest {
		return nil
	}
	return []lockfile.DriftFinding{{
		Kind:     lockfile.DriftKindUpstreamRotation,
		Subject:  ref,
		Expected: lf.Mirror.Upstream.OCIManifestDigest,
		Actual:   got,
		Note:     "upstream OCI manifest digest changed under the same tag",
	}}
}

// compareImageDigest produces an upstream-rotation finding if got differs
// from expected. Returns nil on match. Pure (no network).
func compareImageDigest(ref, expected, got string) *lockfile.DriftFinding {
	if got == expected {
		return nil
	}
	return &lockfile.DriftFinding{
		Kind:     lockfile.DriftKindUpstreamRotation,
		Subject:  ref,
		Expected: expected,
		Actual:   got,
		Note:     "upstream image manifest digest changed under the same ref",
	}
}

// compareDownstreamImageDigest produces a downstream-tampered finding if
// got differs from expected. Returns nil on match. Pure (no network).
func compareDownstreamImageDigest(ref, expected, got string) *lockfile.DriftFinding {
	if got == expected {
		return nil
	}
	return &lockfile.DriftFinding{
		Kind:     lockfile.DriftKindDownstreamTampered,
		Subject:  ref,
		Expected: expected,
		Actual:   got,
		Note:     "downstream image manifest digest no longer matches the mirrored value",
	}
}

func checkImagesUpstream(lf lockfile.File) []lockfile.DriftFinding {
	type job struct {
		idx int
		img lockfile.Image
	}
	jobs := make([]job, 0, len(lf.Mirror.Images))
	for i, img := range lf.Mirror.Images {
		if img.Digest == "" {
			continue
		}
		jobs = append(jobs, job{idx: i, img: img})
	}
	results := make([]*lockfile.DriftFinding, len(jobs))
	parallel(len(jobs), func(i int) {
		got, err := crane.Digest(jobs[i].img.Ref, craneOpts()...)
		if err != nil {
			return
		}
		results[i] = compareImageDigest(jobs[i].img.Ref, jobs[i].img.Digest, got)
	})

	out := make([]lockfile.DriftFinding, 0, len(jobs))
	for _, f := range results {
		if f != nil {
			out = append(out, *f)
		}
	}
	return out
}

func checkChartDownstream(lf lockfile.File) []lockfile.DriftFinding {
	if lf.Mirror.Downstream.Ref == "" || lf.Mirror.Downstream.OCIManifestDigest == "" {
		return nil
	}
	got, err := crane.Digest(lf.Mirror.Downstream.Ref, craneOpts()...)
	if err != nil {
		return nil
	}
	return compareChartDownstreamDigest(lf.Mirror.Downstream.Ref, lf.Mirror.Downstream.OCIManifestDigest, got)
}

// compareChartDownstreamDigest compares a downstream chart manifest digest
// against the lockfile. Pure (no network).
func compareChartDownstreamDigest(ref, expected, got string) []lockfile.DriftFinding {
	if got == expected {
		return nil
	}
	return []lockfile.DriftFinding{{
		Kind:     lockfile.DriftKindDownstreamTampered,
		Subject:  ref,
		Expected: expected,
		Actual:   got,
		Note:     "downstream chart manifest digest no longer matches the mirrored value",
	}}
}

func checkImagesDownstream(lf lockfile.File) []lockfile.DriftFinding {
	type job struct {
		ref, digest string
	}
	jobs := make([]job, 0, len(lf.Mirror.Images))
	for _, img := range lf.Mirror.Images {
		if img.DownstreamRef == "" || img.DownstreamDigest == "" {
			continue
		}
		jobs = append(jobs, job{ref: img.DownstreamRef, digest: img.DownstreamDigest})
	}
	results := make([]*lockfile.DriftFinding, len(jobs))
	parallel(len(jobs), func(i int) {
		got, err := crane.Digest(jobs[i].ref, craneOpts()...)
		if err != nil {
			return
		}
		results[i] = compareDownstreamImageDigest(jobs[i].ref, jobs[i].digest, got)
	})

	out := make([]lockfile.DriftFinding, 0, len(jobs))
	for _, f := range results {
		if f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// checkNewVersions looks for higher semvers than the chart's pinned
// version. Image versions are intentionally out of scope — each image has
// its own release cadence and a single drift report shouldn't conflate
// chart upgrades with per-image upgrades.
func checkNewVersions(cf chartfile.File, lf lockfile.File) []lockfile.DriftFinding {
	var tags []string
	switch cf.Mirror.Upstream.Type {
	case chartfile.TypeRepo:
		idx, err := loadIndex(cf.Mirror.Upstream.URL)
		if err != nil {
			return nil
		}
		for _, cv := range idx.Entries[cf.Mirror.Upstream.Name] {
			tags = append(tags, cv.Version)
		}
	case chartfile.TypeOCI:
		repoName := strings.TrimPrefix(cf.Mirror.Upstream.URL, "oci://")
		t, err := crane.ListTags(repoName, craneOpts()...)
		if err != nil {
			return nil
		}
		tags = t
	default:
		return nil
	}
	return compareNewVersions(cf.Mirror.Upstream.Version, lf.Mirror.Chart.Name, tags)
}

// compareNewVersions filters tags for valid, non-prerelease semvers higher
// than current, and returns at most one finding pointing at the latest.
// Pure (no network).
func compareNewVersions(currentVersion, subject string, tags []string) []lockfile.DriftFinding {
	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		return nil
	}
	var higher []*semver.Version
	for _, t := range tags {
		v, err := semver.NewVersion(t)
		if err != nil {
			continue
		}
		if v.GreaterThan(current) && v.Prerelease() == "" {
			higher = append(higher, v)
		}
	}
	if len(higher) == 0 {
		return nil
	}
	sort.Slice(higher, func(i, j int) bool { return higher[i].LessThan(higher[j]) })
	latest := higher[len(higher)-1]
	return []lockfile.DriftFinding{{
		Kind:     lockfile.DriftKindNewVersionAvailable,
		Subject:  subject,
		Expected: currentVersion,
		Actual:   latest.Original(),
		Note:     fmt.Sprintf("%d newer release(s) available; latest is %s", len(higher), latest.Original()),
	}}
}

func loadIndex(repoURL string) (*repo.IndexFile, error) {
	settings := cli.New()
	chartRepo, err := repo.NewChartRepository(&repo.Entry{URL: repoURL}, getter.All(settings))
	if err != nil {
		return nil, err
	}
	chartRepo.CachePath = settings.RepositoryCache
	if err := os.MkdirAll(chartRepo.CachePath, 0o755); err != nil {
		return nil, err
	}
	indexPath, err := chartRepo.DownloadIndexFile()
	if err != nil {
		return nil, err
	}
	return repo.LoadIndexFile(indexPath)
}

func craneOpts() []crane.Option {
	if insecure.Enabled() {
		return []crane.Option{crane.Insecure}
	}
	return nil
}

func parallel(n int, fn func(i int)) {
	if n == 0 {
		return
	}
	sem := make(chan struct{}, MaxParallel)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// Helps the crane name parser accept localhost when MHELM_INSECURE is set.
var _ = name.WithDefaultRegistry
