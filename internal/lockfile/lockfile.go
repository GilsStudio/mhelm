package lockfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

const SchemaVersion = 1

type File struct {
	SchemaVersion int        `json:"schemaVersion"`
	Chart         Chart      `json:"chart"`
	Upstream      Upstream   `json:"upstream"`
	Downstream    Downstream `json:"downstream"`
	Images        []Image    `json:"images,omitempty"`
	Mirror        Mirror     `json:"mirror"`
}

// Image is a container image referenced by the chart's rendered manifests.
// Discovered by `mhelm discover`; mirrored by `mhelm mirror`.
type Image struct {
	Ref              string       `json:"ref"`
	Digest           string       `json:"digest,omitempty"`
	Source           string       `json:"source,omitempty"` // one of the Source* constants
	ValuesPaths      []ValuesPath `json:"valuesPaths,omitempty"`
	DownstreamRef    string       `json:"downstreamRef,omitempty"`
	DownstreamDigest string       `json:"downstreamDigest,omitempty"`
}

// Source labels record *how* an image was discovered. Used by reviewers to
// understand the audit trail; ordering reflects increasing fragility.
const (
	SourceManifest   = "manifest"   // containers[].image / initContainers[].image
	SourceAnnotation = "annotation" // Chart.yaml artifacthub.io/images
	SourceManual     = "manual"     // chart.json extraImages[]
	SourceEnv        = "env"        // containers[].env[].value (regex + validated)
	SourceConfigMap  = "configmap"  // any ConfigMap data value (regex + validated)
	SourceCRDSpec    = "crd-spec"   // non-builtin kind walked (regex + validated)
)

// ValuesPath is a dotted path in the chart's merged values that produces
// the parent Image's reference.
type ValuesPath struct {
	Path     string `json:"path"`
	Accuracy string `json:"accuracy"`
}

const (
	AccuracyHeuristic = "heuristic"
	AccuracyManual    = "manual"
)

type Chart struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Upstream struct {
	Type               string `json:"type"`
	URL                string `json:"url"`
	ChartContentDigest string `json:"chartContentDigest"`
	OCIManifestDigest  string `json:"ociManifestDigest,omitempty"`
}

type Downstream struct {
	Ref               string `json:"ref"`
	OCIManifestDigest string `json:"ociManifestDigest"`
}

type Mirror struct {
	Tool      string    `json:"tool"`
	Version   string    `json:"version"`
	Timestamp time.Time `json:"timestamp"`
}

// ContentDigest returns the sha256 of b prefixed with "sha256:".
func ContentDigest(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// HexFromDigest strips the "sha256:" prefix, returning raw hex (matches the
// form Helm's index.yaml uses for chart digests).
func HexFromDigest(d string) string {
	return strings.TrimPrefix(d, "sha256:")
}

func Write(path string, f File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Read loads an existing chart-lock.json. Returns os.ErrNotExist when the
// file does not exist — callers may use errors.Is to treat that as
// "fresh start".
func Read(path string) (File, error) {
	var f File
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	return f, nil
}

// IsNotExist reports whether err signals a missing lockfile (sugar for
// errors.Is(err, os.ErrNotExist)).
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
