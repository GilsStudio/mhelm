// Package mirror pushes a Helm chart .tgz to the downstream OCI registry
// via helm's registry client (which wraps oras-go with Helm media types).
package mirror

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/chartpull"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
)

type Result struct {
	ChartName                string
	ChartVersion             string
	ChartContentDigest       string // sha256:... of the .tgz bytes (registry-agnostic identity)
	UpstreamManifestDigest   string // sha256:... — empty for repo type
	DownstreamRef            string // <registry>/<path>/<name>:<version>
	DownstreamManifestDigest string // sha256:...
}

// Run mirrors the chart described by cf to its downstream OCI destination
// and returns the digests needed to populate chart-lock.json.
func Run(ctx context.Context, cf chartfile.File) (Result, error) {
	var res Result

	pulled, err := chartpull.Pull(ctx, cf.Upstream)
	if err != nil {
		return res, fmt.Errorf("pull: %w", err)
	}
	tgz := pulled.Bytes
	res.UpstreamManifestDigest = pulled.OCIManifestDigest
	res.ChartContentDigest = lockfile.ContentDigest(tgz)

	meta, err := loadChartMeta(tgz)
	if err != nil {
		return res, fmt.Errorf("load chart metadata: %w", err)
	}
	res.ChartName = meta.Name
	res.ChartVersion = meta.Version

	settings := cli.New()
	client, err := registry.NewClient(
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
	)
	if err != nil {
		return res, fmt.Errorf("registry client: %w", err)
	}

	destRef := fmt.Sprintf("%s/%s:%s",
		strings.TrimPrefix(cf.Downstream.URL, "oci://"),
		res.ChartName,
		res.ChartVersion,
	)
	pushRes, err := client.Push(tgz, destRef)
	if err != nil {
		return res, fmt.Errorf("push %s: %w", destRef, err)
	}
	res.DownstreamRef = destRef
	if pushRes != nil && pushRes.Manifest != nil {
		res.DownstreamManifestDigest = pushRes.Manifest.Digest
	}
	return res, nil
}

type chartMeta struct {
	Name    string
	Version string
}

func loadChartMeta(tgz []byte) (chartMeta, error) {
	c, err := loader.LoadArchive(bytes.NewReader(tgz))
	if err != nil {
		return chartMeta{}, err
	}
	if c.Metadata == nil {
		return chartMeta{}, fmt.Errorf("chart has no metadata")
	}
	return chartMeta{Name: c.Metadata.Name, Version: c.Metadata.Version}, nil
}
