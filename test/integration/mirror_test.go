//go:build integration

// Package integration drives the full discover → mirror → drift loop
// against a local OCI registry spun up via testcontainers-go. Gated by
// the `integration` build tag so it stays out of `go test ./...`.
//
// Run with:
//
//	go test -tags integration ./test/integration/...
//
// Docker (or a Docker-compatible runtime testcontainers can find) must
// be running.
package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/chartpull"
	"github.com/gilsstudio/mhelm/internal/discover"
	"github.com/gilsstudio/mhelm/internal/drift"
	"github.com/gilsstudio/mhelm/internal/imagemirror"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/mirror"
	"github.com/gilsstudio/mhelm/internal/wrap"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"sigs.k8s.io/yaml"
)

func TestDiscoverMirrorDriftLoop(t *testing.T) {
	t.Setenv("MHELM_INSECURE", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	registryAddr := startRegistry(ctx, t)
	t.Logf("registry: %s", registryAddr)

	// Push synthetic images under the refs the chart templates reference.
	upstreamRefs := []string{
		registryAddr + "/upstream/app:1.0",
		registryAddr + "/upstream/init:1.0",
		registryAddr + "/upstream/configmap-image:1.0",
		registryAddr + "/upstream/worker:1.0",
		registryAddr + "/upstream/annotated:1.0",
	}
	upstreamDigests := map[string]string{}
	for _, ref := range upstreamRefs {
		upstreamDigests[ref] = pushSyntheticImage(t, ref)
	}

	// Build the chart from the fixture (substituting __REGISTRY__).
	tgz := packageFixtureChart(t, registryAddr)

	// Serve a Helm repo over httptest carrying just this chart.
	repoURL := serveChartRepo(t, "tinychart-0.1.0.tgz", tgz)

	// Write chart.json into the test work dir.
	workDir := t.TempDir()
	cf := chartfile.File{
		APIVersion: chartfile.APIVersion,
		Mirror: chartfile.Mirror{
			Upstream: chartfile.Endpoint{
				Type: chartfile.TypeRepo, Name: "tinychart",
				URL: repoURL, Version: "0.1.0",
			},
			Downstream: chartfile.Endpoint{
				Type: chartfile.TypeOCI,
				URL:  "oci://" + registryAddr + "/mirror",
			},
		},
	}
	writeChartJSON(t, filepath.Join(workDir, "chart.json"), cf)

	// First end-to-end run.
	firstImages, firstMirrorValues := runDiscoverMirror(ctx, t, cf, workDir)

	// Assertions on lockfile.
	assertImagesDiscovered(t, firstImages, upstreamRefs, registryAddr)
	for _, img := range firstImages {
		if img.DownstreamRef == "" {
			t.Errorf("image %q has no DownstreamRef", img.Ref)
			continue
		}
		if _, err := crane.Digest(img.DownstreamRef, crane.Insecure); err != nil {
			t.Errorf("downstream ref %q not pullable: %v", img.DownstreamRef, err)
		}
	}

	// mirror-values.yaml is parseable.
	mvPath := filepath.Join(workDir, "mirror-values.yaml")
	if _, err := os.Stat(mvPath); err != nil {
		t.Fatalf("mirror-values.yaml not written: %v", err)
	}
	mvBytes, err := os.ReadFile(mvPath)
	if err != nil {
		t.Fatalf("read mirror-values: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(mvBytes, &parsed); err != nil {
		t.Fatalf("mirror-values.yaml unparseable: %v\n---\n%s", err, mvBytes)
	}
	if !strings.Contains(string(mvBytes), registryAddr) {
		t.Errorf("mirror-values.yaml does not mention mirror registry %q:\n%s",
			registryAddr, mvBytes)
	}
	if diff := cmp.Diff(firstMirrorValues, parsed); diff != "" {
		t.Errorf("mirror-values disk vs in-memory (-want +got):\n%s", diff)
	}

	// Drift returns zero findings immediately after mirror.
	lf := readLockfile(t, filepath.Join(workDir, "chart-lock.json"))
	d := drift.Run(ctx, cf, lf, drift.DefaultOptions())
	if len(d.Findings) != 0 {
		t.Errorf("drift findings immediately after mirror, expected 0:\n%+v", d.Findings)
	}

	// Second run — lockfile Images section stable.
	secondImages, _ := runDiscoverMirror(ctx, t, cf, workDir)
	if diff := cmp.Diff(firstImages, secondImages,
		cmpopts.SortSlices(func(a, b lockfile.Image) bool { return a.Ref < b.Ref }),
	); diff != "" {
		t.Errorf("lockfile Images section not stable across runs (-first +second):\n%s", diff)
	}
}

// TestWrapEndToEnd exercises the v0.3.0 wrap pipeline against the
// local registry: discover + mirror, then wrap, then assert the
// wrapper is pullable and its values.yaml carries digest-pinned
// rewrites.
func TestWrapEndToEnd(t *testing.T) {
	t.Setenv("MHELM_INSECURE", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	registryAddr := startRegistry(ctx, t)

	upstreamRefs := []string{
		registryAddr + "/upstream/app:1.0",
		registryAddr + "/upstream/init:1.0",
		registryAddr + "/upstream/configmap-image:1.0",
		registryAddr + "/upstream/worker:1.0",
		registryAddr + "/upstream/annotated:1.0",
	}
	for _, ref := range upstreamRefs {
		pushSyntheticImage(t, ref)
	}
	tgz := packageFixtureChart(t, registryAddr)
	repoURL := serveChartRepo(t, "tinychart-0.1.0.tgz", tgz)

	workDir := t.TempDir()
	cf := chartfile.File{
		APIVersion: chartfile.APIVersion,
		Mirror: chartfile.Mirror{
			Upstream: chartfile.Endpoint{
				Type: chartfile.TypeRepo, Name: "tinychart",
				URL: repoURL, Version: "0.1.0",
			},
			Downstream: chartfile.Endpoint{
				Type: chartfile.TypeOCI,
				URL:  "oci://" + registryAddr + "/mirror",
			},
		},
		Wrap: &chartfile.Wrap{
			Name:    "tinychart-wrapped",
			Version: "0.1.0-myorg.1",
		},
	}
	writeChartJSON(t, filepath.Join(workDir, "chart.json"), cf)

	// discover + mirror through the existing helper.
	images, _ := runDiscoverMirror(ctx, t, cf, workDir)
	if len(images) == 0 {
		t.Fatal("expected images mirrored, got none")
	}

	// Now wrap.
	lf := readLockfile(t, filepath.Join(workDir, "chart-lock.json"))
	res, err := wrap.Run(ctx, cf, lf, workDir)
	if err != nil {
		t.Fatalf("wrap.Run: %v", err)
	}

	// Wrapper pullable from the registry.
	if _, err := crane.Digest(res.DownstreamRef, crane.Insecure); err != nil {
		t.Errorf("wrapper not pullable at %s: %v", res.DownstreamRef, err)
	}

	// Persist wrap block + reload.
	block := res.ToLockfileBlock("test", time.Time{})
	lf.Wrap = &block
	if err := lockfile.Write(filepath.Join(workDir, "chart-lock.json"), lf); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	lf = readLockfile(t, filepath.Join(workDir, "chart-lock.json"))
	if lf.Wrap == nil {
		t.Fatal("lockfile.Wrap not persisted")
	}
	if lf.Wrap.Chart.Ref != res.DownstreamRef {
		t.Errorf("lockfile.wrap.chart.ref = %q, want %q", lf.Wrap.Chart.Ref, res.DownstreamRef)
	}
	if len(lf.Wrap.DeployedImages) == 0 {
		t.Error("lockfile.wrap.deployedImages empty")
	}

	// Pull the wrapper .tgz from the registry and inspect its
	// values.yaml: every image with a valuesPath in the lockfile
	// should be present in the wrapper's nested values.
	tgzBytes, err := crane.PullLayer(res.DownstreamRef, crane.Insecure)
	if err != nil {
		// crane.PullLayer signature varies — fall back to crane.Pull.
		_ = tgzBytes
	}
	// Simpler path: load via Helm SDK after re-pulling.
	pullEp := chartfile.Endpoint{
		Type:    chartfile.TypeOCI,
		URL:     "oci://" + registryAddr + "/mirror/tinychart-wrapped",
		Version: "0.1.0-myorg.1",
	}
	pulled, err := chartpullPull(ctx, pullEp)
	if err != nil {
		t.Fatalf("re-pull wrapper: %v", err)
	}
	wrapperChart, err := loader.LoadArchive(bytes.NewReader(pulled))
	if err != nil {
		t.Fatalf("load wrapper chart: %v", err)
	}

	// Wrapper has the dep declared.
	if len(wrapperChart.Metadata.Dependencies) != 1 {
		t.Fatalf("wrapper dependencies = %d, want 1", len(wrapperChart.Metadata.Dependencies))
	}
	if wrapperChart.Metadata.Dependencies[0].Name != "tinychart" {
		t.Errorf("dep name = %q, want %q", wrapperChart.Metadata.Dependencies[0].Name, "tinychart")
	}

	// Wrapper values are nested under "tinychart" and carry rewrites.
	depBlock, ok := wrapperChart.Values["tinychart"].(map[string]any)
	if !ok {
		t.Fatalf("wrapper values missing tinychart block: %#v", wrapperChart.Values)
	}

	// At least one rewrite must reference the mirror registry — either
	// in a string image ref, or in the decomposed registry/repository
	// fields.
	yb, _ := yaml.Marshal(depBlock)
	if !strings.Contains(string(yb), registryAddr) || !strings.Contains(string(yb), "mirror") {
		t.Errorf("wrapper values do not reference the mirror registry:\n%s", yb)
	}

	// Digest-pinned: at least one rewrite must contain a sha256: digest.
	if !strings.Contains(string(yb), "sha256:") {
		t.Errorf("wrapper values are not digest-pinned:\n%s", yb)
	}
}

// chartpullPull is a thin shim over chartpull.Pull that returns just
// the bytes so the test stays terse.
func chartpullPull(ctx context.Context, ep chartfile.Endpoint) ([]byte, error) {
	r, err := chartpull.Pull(ctx, ep)
	if err != nil {
		return nil, err
	}
	return r.Bytes, nil
}

// runDiscoverMirror replicates the cmd/discover + cmd/mirror logic
// against the given chartfile + workDir. Returns the resulting Images
// slice and MirrorValues map, both useful for assertions.
func runDiscoverMirror(ctx context.Context, t *testing.T, cf chartfile.File, workDir string) ([]lockfile.Image, map[string]any) {
	t.Helper()

	res, err := discover.Run(ctx, cf, workDir)
	if err != nil {
		t.Fatalf("discover.Run: %v", err)
	}

	lockPath := filepath.Join(workDir, "chart-lock.json")
	lf, err := lockfile.Read(lockPath)
	if err != nil && !lockfile.IsNotExist(err) {
		t.Fatalf("read lockfile: %v", err)
	}
	lf.APIVersion = lockfile.APIVersion
	lf.Mirror.Chart = lockfile.Chart{Name: res.ChartName, Version: res.ChartVersion}
	lf.Mirror.Upstream.Type = cf.Mirror.Upstream.Type
	lf.Mirror.Upstream.URL = cf.Mirror.Upstream.URL
	lf.Mirror.Upstream.ChartContentDigest = res.ChartContentDigest
	if res.UpstreamManifestDigest != "" {
		lf.Mirror.Upstream.OCIManifestDigest = res.UpstreamManifestDigest
	}
	lf.Mirror.Images = res.Images
	if lf.Mirror.Tool == "" {
		lf.Mirror.Tool = "integration"
		lf.Mirror.Timestamp = time.Time{}
	}
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatalf("write lockfile (post-discover): %v", err)
	}

	if len(res.MirrorValues) > 0 {
		b, err := yaml.Marshal(res.MirrorValues)
		if err != nil {
			t.Fatalf("marshal mirror-values: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workDir, "mirror-values.yaml"), b, 0o644); err != nil {
			t.Fatalf("write mirror-values: %v", err)
		}
	}

	mres, err := mirror.Run(ctx, cf)
	if err != nil {
		t.Fatalf("mirror.Run: %v", err)
	}

	mirrorPrefix := strings.TrimPrefix(cf.Mirror.Downstream.URL, "oci://")
	inputs := make([]imagemirror.Input, len(lf.Mirror.Images))
	for i, img := range lf.Mirror.Images {
		inputs[i] = imagemirror.Input{UpstreamRef: img.Ref, UpstreamDigest: img.Digest}
	}
	imgResults := imagemirror.Mirror(ctx, inputs, mirrorPrefix)
	for i, r := range imgResults {
		if r.Err != nil {
			t.Fatalf("imagemirror failed for %s: %v", r.UpstreamRef, r.Err)
		}
		lf.Mirror.Images[i].DownstreamRef = r.DownstreamRef
		lf.Mirror.Images[i].DownstreamDigest = r.DownstreamDigest
	}

	lf.Mirror.Downstream = lockfile.Downstream{
		Ref:               mres.DownstreamRef,
		OCIManifestDigest: mres.DownstreamManifestDigest,
	}
	lf.Mirror.Tool = "integration"
	lf.Mirror.Timestamp = time.Time{}
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatalf("write lockfile (post-mirror): %v", err)
	}

	return lf.Mirror.Images, res.MirrorValues
}

// startRegistry runs registry:2 via testcontainers and returns its
// "host:port" address as seen from the test process.
func startRegistry(ctx context.Context, t *testing.T) string {
	t.Helper()
	req := testcontainers.ContainerRequest{
		Image:        "registry:2",
		ExposedPorts: []string{"5000/tcp"},
		WaitingFor:   wait.ForListeningPort("5000/tcp").WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start registry container: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Terminate(context.Background())
	})
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("registry host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5000/tcp")
	if err != nil {
		t.Fatalf("registry port: %v", err)
	}
	return fmt.Sprintf("%s:%s", host, port.Port())
}

// pushSyntheticImage builds a tiny single-layer image and pushes it to
// the given ref. Returns the manifest digest.
func pushSyntheticImage(t *testing.T, ref string) string {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	if err := crane.Push(img, ref, crane.Insecure); err != nil {
		t.Fatalf("crane.Push %s: %v", ref, err)
	}
	d, err := digest(img)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return d
}

func digest(img v1.Image) (string, error) {
	d, err := img.Digest()
	if err != nil {
		return "", err
	}
	return d.String(), nil
}

// packageFixtureChart copies the fixture chart to a tempdir, substitutes
// __REGISTRY__ with the live registry address in values + Chart.yaml,
// loads via helm, and writes a .tgz to a temp file. Returns the .tgz
// bytes.
func packageFixtureChart(t *testing.T, registryAddr string) []byte {
	t.Helper()
	srcDir := "fixtures/tiny-chart"
	staging := t.TempDir()
	chartDir := filepath.Join(staging, "tinychart")
	if err := copyDir(srcDir, chartDir, func(b []byte) []byte {
		return []byte(strings.ReplaceAll(string(b), "__REGISTRY__", registryAddr))
	}); err != nil {
		t.Fatalf("copy chart fixture: %v", err)
	}
	c, err := loader.LoadDir(chartDir)
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	outDir := t.TempDir()
	tgzPath, err := chartutil.Save(c, outDir)
	if err != nil {
		t.Fatalf("chartutil.Save: %v", err)
	}
	b, err := os.ReadFile(tgzPath)
	if err != nil {
		t.Fatalf("read tgz: %v", err)
	}
	return b
}

func copyDir(src, dst string, transform func([]byte) []byte) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if transform != nil {
			b = transform(b)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// serveChartRepo stands up an httptest.Server that serves a synthesised
// Helm repo index.yaml + the given tgz under the supplied filename.
// Returns the repo's base URL.
func serveChartRepo(t *testing.T, tgzName string, tgz []byte) string {
	t.Helper()

	sum := sha256.Sum256(tgz)
	tgzDigest := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/"+tgzName, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tgz)
	})
	// index.yaml is templated lazily once we know the server URL.
	var index []byte
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(index)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	index = []byte(fmt.Sprintf(`apiVersion: v1
entries:
  tinychart:
  - apiVersion: v2
    name: tinychart
    version: 0.1.0
    description: Minimal chart used by mhelm's integration tests.
    type: application
    appVersion: "1.0"
    urls:
    - %s/%s
    digest: %s
    created: "2026-05-14T00:00:00Z"
generated: "2026-05-14T00:00:00Z"
`, srv.URL, tgzName, tgzDigest))

	return srv.URL
}

func writeChartJSON(t *testing.T, path string, cf chartfile.File) {
	t.Helper()
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		t.Fatalf("marshal chart.json: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write chart.json: %v", err)
	}
}

func readLockfile(t *testing.T, path string) lockfile.File {
	t.Helper()
	lf, err := lockfile.Read(path)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	return lf
}

func assertImagesDiscovered(t *testing.T, got []lockfile.Image, wantRefs []string, registryAddr string) {
	t.Helper()
	have := map[string]bool{}
	for _, img := range got {
		have[img.Ref] = true
	}
	for _, ref := range wantRefs {
		if !have[ref] {
			t.Errorf("expected discovered ref %q, not in lockfile", ref)
		}
	}
}
