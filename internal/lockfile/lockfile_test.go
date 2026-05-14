package lockfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestContentDigest(t *testing.T) {
	// RFC 6234 / FIPS-180 test vector: SHA-256 of "abc" is the well-known
	// ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad.
	in := []byte("abc")
	want := "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := ContentDigest(in); got != want {
		t.Errorf("ContentDigest(%q) = %q, want %q", in, got, want)
	}
	// Sanity-check empty input.
	emptyHash := sha256.Sum256(nil)
	wantEmpty := "sha256:" + hex.EncodeToString(emptyHash[:])
	if got := ContentDigest(nil); got != wantEmpty {
		t.Errorf("ContentDigest(nil) = %q, want %q", got, wantEmpty)
	}
}

func TestHexFromDigest(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"sha256:abc", "abc"},
		{"abc", "abc"},
		{"sha512:abc", "sha512:abc"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := HexFromDigest(tc.in); got != tc.want {
			t.Errorf("HexFromDigest(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsNotExist(t *testing.T) {
	if !IsNotExist(os.ErrNotExist) {
		t.Error("IsNotExist(os.ErrNotExist) = false, want true")
	}
	if !IsNotExist(&os.PathError{Err: os.ErrNotExist}) {
		t.Error("IsNotExist(wrapped os.ErrNotExist) = false, want true")
	}
	if IsNotExist(io.EOF) {
		t.Error("IsNotExist(io.EOF) = true, want false")
	}
	if IsNotExist(errors.New("some other error")) {
		t.Error("IsNotExist(other) = true, want false")
	}
	if IsNotExist(nil) {
		t.Error("IsNotExist(nil) = true, want false")
	}
}

func TestReadWriteRoundtrip(t *testing.T) {
	want := buildGoldenFile()
	dir := t.TempDir()
	path := filepath.Join(dir, "chart-lock.json")
	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("roundtrip mismatch (-want +got):\n%s", diff)
	}
}

func TestReadNonExistent(t *testing.T) {
	dir := t.TempDir()
	_, err := Read(filepath.Join(dir, "nope.json"))
	if !IsNotExist(err) {
		t.Errorf("Read of missing file: IsNotExist(%v) = false, want true", err)
	}
}

// TestGoldenLockfileStable is the load-bearing test: lockfile bytes are
// the audit trail. Silent reordering of fields would poison every
// downstream PR review. Any intentional change to the JSON shape must
// update testdata/golden-lockfile.json by running with
// MHELM_UPDATE_GOLDEN=1.
func TestGoldenLockfileStable(t *testing.T) {
	want := buildGoldenFile()
	dir := t.TempDir()
	path := filepath.Join(dir, "chart-lock.json")
	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}

	goldenPath := "testdata/golden-lockfile.json"
	if os.Getenv("MHELM_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, gotBytes, 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with MHELM_UPDATE_GOLDEN=1 to create): %v", err)
	}
	if string(wantBytes) != string(gotBytes) {
		t.Errorf("golden lockfile bytes differ.\n--- want\n%s\n--- got\n%s",
			wantBytes, gotBytes)
	}
}

// buildGoldenFile constructs a fully-populated File. Every optional field
// is set so the golden test catches changes to any of them.
func buildGoldenFile() File {
	ts := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	checked := time.Date(2026, 5, 14, 1, 0, 0, 0, time.UTC)
	return File{
		APIVersion: APIVersion,
		Mirror: MirrorBlock{
			Chart: Chart{Name: "tinychart", Version: "0.1.0"},
			Upstream: Upstream{
				Type:               "repo",
				URL:                "https://example.com/charts",
				ChartContentDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				OCIManifestDigest:  "sha256:deadbeef00000000000000000000000000000000000000000000000000000000",
				Signature: &Signature{
					Verified:      true,
					Type:          "cosign-keyless",
					Subject:       "https://github.com/org/repo/.github/workflows/release.yml@refs/heads/main",
					Issuer:        "https://token.actions.githubusercontent.com",
					RekorLogIndex: 12345,
				},
			},
			Downstream: Downstream{
				Ref:               "ghcr.io/mirror/tinychart:0.1.0",
				OCIManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			},
			Images: []Image{
				{
					Ref:           "registry.io/app:1",
					Digest:        "sha256:2222222222222222222222222222222222222222222222222222222222222222",
					Source:        SourceManifest,
					DiscoveredVia: DiscoveredViaDiscoveryValues,
					ValuesPaths: []ValuesPath{
						{Path: "image", Accuracy: AccuracyHeuristic},
					},
					DownstreamRef:    "ghcr.io/mirror/app:1",
					DownstreamDigest: "sha256:3333333333333333333333333333333333333333333333333333333333333333",
					Signature: &Signature{
						Verified: false,
						Type:     "none",
					},
				},
			},
			Tool:      "mhelm",
			Version:   "v0.1.0",
			Timestamp: ts,
		},
		Drift: &Drift{
			CheckedAt: checked,
			Findings: []DriftFinding{
				{
					Kind:     DriftKindUpstreamRotation,
					Subject:  "registry.io/app:1",
					Expected: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
					Actual:   "sha256:badbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbad0",
					Note:     "upstream image manifest digest changed under the same ref",
				},
			},
		},
	}
}

// TestReadMigratesV01 confirms v0.1.0-shaped lockfiles still parse and
// land in the new mirror block.
func TestReadMigratesV01(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chart-lock.json")
	v01 := `{
  "schemaVersion": 1,
  "chart": {"name": "tinychart", "version": "0.1.0"},
  "upstream": {"type": "repo", "url": "https://example.com/charts", "chartContentDigest": "sha256:abc"},
  "downstream": {"ref": "ghcr.io/mirror/tinychart:0.1.0", "ociManifestDigest": "sha256:def"},
  "images": [{"ref": "x/y:1", "digest": "sha256:111", "source": "manifest"}],
  "mirror": {"tool": "mhelm", "version": "v0.1.0", "timestamp": "2026-05-14T00:00:00Z"}
}`
	if err := os.WriteFile(path, []byte(v01), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if f.APIVersion != APIVersion {
		t.Errorf("APIVersion = %q, want %q", f.APIVersion, APIVersion)
	}
	if f.Mirror.Chart.Name != "tinychart" {
		t.Errorf("mirror.chart.name not migrated: %q", f.Mirror.Chart.Name)
	}
	if len(f.Mirror.Images) != 1 || f.Mirror.Images[0].Ref != "x/y:1" {
		t.Errorf("mirror.images not migrated: %+v", f.Mirror.Images)
	}
	if f.Mirror.Tool != "mhelm" {
		t.Errorf("mirror.tool not migrated: %q", f.Mirror.Tool)
	}
}
