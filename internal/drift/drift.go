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
	switch cf.Upstream.Type {
	case chartfile.TypeRepo:
		return checkChartUpstreamRepo(cf, lf)
	case chartfile.TypeOCI:
		return checkChartUpstreamOCI(cf, lf)
	}
	return nil
}

func checkChartUpstreamRepo(cf chartfile.File, lf lockfile.File) []lockfile.DriftFinding {
	idx, err := loadIndex(cf.Upstream.URL)
	if err != nil {
		return nil
	}
	cv, err := idx.Get(cf.Upstream.Name, cf.Upstream.Version)
	if err != nil {
		return []lockfile.DriftFinding{{
			Kind:    lockfile.DriftKindUpstreamMissing,
			Subject: cf.Upstream.Name + "@" + cf.Upstream.Version,
			Note:    "upstream index.yaml no longer lists this chart version",
		}}
	}
	expected := strings.TrimPrefix(lf.Upstream.ChartContentDigest, "sha256:")
	if expected == "" || strings.EqualFold(cv.Digest, expected) {
		return nil
	}
	return []lockfile.DriftFinding{{
		Kind:     lockfile.DriftKindUpstreamRotation,
		Subject:  cf.Upstream.Name + "@" + cf.Upstream.Version,
		Expected: lf.Upstream.ChartContentDigest,
		Actual:   "sha256:" + cv.Digest,
		Note:     "upstream index.yaml now reports a different digest for this chart version",
	}}
}

func checkChartUpstreamOCI(cf chartfile.File, lf lockfile.File) []lockfile.DriftFinding {
	if lf.Upstream.OCIManifestDigest == "" {
		return nil
	}
	ref := strings.TrimPrefix(cf.Upstream.URL, "oci://") + ":" + cf.Upstream.Version
	got, err := crane.Digest(ref, craneOpts()...)
	if err != nil {
		return nil
	}
	if got == lf.Upstream.OCIManifestDigest {
		return nil
	}
	return []lockfile.DriftFinding{{
		Kind:     lockfile.DriftKindUpstreamRotation,
		Subject:  ref,
		Expected: lf.Upstream.OCIManifestDigest,
		Actual:   got,
		Note:     "upstream OCI manifest digest changed under the same tag",
	}}
}

func checkImagesUpstream(lf lockfile.File) []lockfile.DriftFinding {
	type job struct {
		idx int
		img lockfile.Image
	}
	type result struct {
		f *lockfile.DriftFinding
	}

	jobs := make([]job, 0, len(lf.Images))
	for i, img := range lf.Images {
		if img.Digest == "" {
			continue
		}
		jobs = append(jobs, job{idx: i, img: img})
	}
	results := make([]result, len(jobs))
	parallel(len(jobs), func(i int) {
		got, err := crane.Digest(jobs[i].img.Ref, craneOpts()...)
		if err != nil {
			return
		}
		if got != jobs[i].img.Digest {
			results[i].f = &lockfile.DriftFinding{
				Kind:     lockfile.DriftKindUpstreamRotation,
				Subject:  jobs[i].img.Ref,
				Expected: jobs[i].img.Digest,
				Actual:   got,
				Note:     "upstream image manifest digest changed under the same ref",
			}
		}
	})

	out := make([]lockfile.DriftFinding, 0, len(jobs))
	for _, r := range results {
		if r.f != nil {
			out = append(out, *r.f)
		}
	}
	return out
}

func checkChartDownstream(lf lockfile.File) []lockfile.DriftFinding {
	if lf.Downstream.Ref == "" || lf.Downstream.OCIManifestDigest == "" {
		return nil
	}
	got, err := crane.Digest(lf.Downstream.Ref, craneOpts()...)
	if err != nil {
		return nil
	}
	if got == lf.Downstream.OCIManifestDigest {
		return nil
	}
	return []lockfile.DriftFinding{{
		Kind:     lockfile.DriftKindDownstreamTampered,
		Subject:  lf.Downstream.Ref,
		Expected: lf.Downstream.OCIManifestDigest,
		Actual:   got,
		Note:     "downstream chart manifest digest no longer matches the mirrored value",
	}}
}

func checkImagesDownstream(lf lockfile.File) []lockfile.DriftFinding {
	type job struct {
		ref, digest string
	}
	jobs := make([]job, 0, len(lf.Images))
	for _, img := range lf.Images {
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
		if got != jobs[i].digest {
			results[i] = &lockfile.DriftFinding{
				Kind:     lockfile.DriftKindDownstreamTampered,
				Subject:  jobs[i].ref,
				Expected: jobs[i].digest,
				Actual:   got,
				Note:     "downstream image manifest digest no longer matches the mirrored value",
			}
		}
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
	current, err := semver.NewVersion(cf.Upstream.Version)
	if err != nil {
		return nil
	}

	var tags []string
	switch cf.Upstream.Type {
	case chartfile.TypeRepo:
		idx, err := loadIndex(cf.Upstream.URL)
		if err != nil {
			return nil
		}
		for _, cv := range idx.Entries[cf.Upstream.Name] {
			tags = append(tags, cv.Version)
		}
	case chartfile.TypeOCI:
		repoName := strings.TrimPrefix(cf.Upstream.URL, "oci://")
		t, err := crane.ListTags(repoName, craneOpts()...)
		if err != nil {
			return nil
		}
		tags = t
	default:
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
		Subject:  lf.Chart.Name,
		Expected: cf.Upstream.Version,
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
