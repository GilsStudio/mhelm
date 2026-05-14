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
	Drift         *Drift     `json:"drift,omitempty"`
}

// Drift captures the result of the most recent `mhelm drift` run.
// Replaced wholesale on each run so the lockfile diff is the audit.
type Drift struct {
	CheckedAt time.Time      `json:"checkedAt"`
	Findings  []DriftFinding `json:"findings,omitempty"`
}

// DriftFinding is one drift signal. Kind is one of the DriftKind* constants.
type DriftFinding struct {
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Note     string `json:"note,omitempty"`
}

const (
	DriftKindUpstreamRotation    = "upstream-rotation"    // upstream now publishes different bytes under the same ref
	DriftKindDownstreamTampered  = "downstream-tampered"  // downstream digest no longer matches what we mirrored
	DriftKindNewVersionAvailable = "new-version-available"
	DriftKindUpstreamMissing     = "upstream-missing" // the pinned upstream entry no longer exists
)

// Image is a container image referenced by the chart's rendered manifests.
// Discovered by `mhelm discover`; mirrored by `mhelm mirror`.
type Image struct {
	Ref              string       `json:"ref"`
	Digest           string       `json:"digest,omitempty"`
	Source           string       `json:"source,omitempty"` // one of the Source* constants
	ValuesPaths      []ValuesPath `json:"valuesPaths,omitempty"`
	DownstreamRef    string       `json:"downstreamRef,omitempty"`
	DownstreamDigest string       `json:"downstreamDigest,omitempty"`
	Signature        *Signature   `json:"signature,omitempty"`
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
	Type               string     `json:"type"`
	URL                string     `json:"url"`
	ChartContentDigest string     `json:"chartContentDigest"`
	OCIManifestDigest  string     `json:"ociManifestDigest,omitempty"`
	Signature          *Signature `json:"signature,omitempty"`
}

// Signature records the result of an upstream-signature verification
// attempt. Written by `mhelm verify`.
type Signature struct {
	// Verified is true only when a signature exists AND the verification
	// path (cert chain + Rekor entry) succeeded. False covers both
	// "not signed" and "signed but verification failed" — Type and Error
	// disambiguate.
	Verified bool `json:"verified"`
	// Type: "cosign-keyless" | "cosign-key" | "none" | "error".
	Type string `json:"type"`
	// Subject / Issuer: the OIDC identity from the Fulcio cert (keyless).
	Subject string `json:"subject,omitempty"`
	Issuer  string `json:"issuer,omitempty"`
	// RekorLogIndex: transparency log entry index for audit.
	RekorLogIndex int64 `json:"rekorLogIndex,omitempty"`
	// Error: non-empty when Type == "error".
	Error string `json:"error,omitempty"`
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
