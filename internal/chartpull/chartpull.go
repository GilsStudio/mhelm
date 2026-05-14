// Package chartpull pulls a Helm chart's .tgz bytes from an upstream
// (classic HTTP repo or OCI), used by `mhelm mirror`.
package chartpull

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/insecure"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/repo"
)

type Result struct {
	Bytes             []byte
	OCIManifestDigest string // empty for upstream.type == "repo"
}

// Pull fetches the chart described by ep. For repo upstreams it also
// cross-checks the downloaded .tgz sha against the digest claimed in
// index.yaml and aborts on mismatch.
func Pull(ctx context.Context, ep chartfile.Endpoint) (Result, error) {
	switch ep.Type {
	case chartfile.TypeRepo:
		b, err := pullFromRepo(ep)
		return Result{Bytes: b}, err
	case chartfile.TypeOCI:
		b, d, err := pullFromOCI(ep)
		return Result{Bytes: b, OCIManifestDigest: d}, err
	default:
		return Result{}, fmt.Errorf("unsupported upstream.type %q", ep.Type)
	}
}

func pullFromRepo(ep chartfile.Endpoint) ([]byte, error) {
	settings := cli.New()
	chartRepo, err := repo.NewChartRepository(&repo.Entry{URL: ep.URL}, getter.All(settings))
	if err != nil {
		return nil, err
	}
	chartRepo.CachePath = settings.RepositoryCache
	if err := os.MkdirAll(chartRepo.CachePath, 0o755); err != nil {
		return nil, err
	}
	indexPath, err := chartRepo.DownloadIndexFile()
	if err != nil {
		return nil, fmt.Errorf("download index from %s: %w", ep.URL, err)
	}
	idx, err := repo.LoadIndexFile(indexPath)
	if err != nil {
		return nil, err
	}
	cv, err := idx.Get(ep.Name, ep.Version)
	if err != nil {
		return nil, fmt.Errorf("find %s@%s in index: %w", ep.Name, ep.Version, err)
	}
	if len(cv.URLs) == 0 {
		return nil, fmt.Errorf("no URLs for %s@%s", ep.Name, ep.Version)
	}
	abs, err := repo.ResolveReferenceURL(ep.URL, cv.URLs[0])
	if err != nil {
		return nil, err
	}
	g, err := getter.NewHTTPGetter()
	if err != nil {
		return nil, err
	}
	buf, err := g.Get(abs)
	if err != nil {
		return nil, err
	}
	tgz := buf.Bytes()

	// Data integrity: cross-check .tgz sha against the digest claimed in index.yaml.
	if cv.Digest != "" {
		got := lockfile.HexFromDigest(lockfile.ContentDigest(tgz))
		if !strings.EqualFold(got, cv.Digest) {
			return nil, fmt.Errorf(
				"chart digest mismatch: index.yaml claims %s, downloaded bytes hash %s",
				cv.Digest, got,
			)
		}
	}
	return tgz, nil
}

func pullFromOCI(ep chartfile.Endpoint) ([]byte, string, error) {
	settings := cli.New()
	opts := []registry.ClientOption{registry.ClientOptCredentialsFile(settings.RegistryConfig)}
	if insecure.Enabled() {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}
	client, err := registry.NewClient(opts...)
	if err != nil {
		return nil, "", fmt.Errorf("registry client: %w", err)
	}
	ref := strings.TrimPrefix(ep.URL, "oci://") + ":" + ep.Version
	res, err := client.Pull(ref, registry.PullOptWithChart(true))
	if err != nil {
		return nil, "", err
	}
	if res == nil || res.Chart == nil || len(res.Chart.Data) == 0 {
		return nil, "", fmt.Errorf("empty chart pulled from %s", ref)
	}
	var manifestDigest string
	if res.Manifest != nil {
		manifestDigest = res.Manifest.Digest
	}
	return res.Chart.Data, manifestDigest, nil
}
