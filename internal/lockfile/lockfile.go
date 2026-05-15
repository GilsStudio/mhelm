// Package lockfile parses and writes chart-lock.json, the generated
// source of truth recording every pinned digest mhelm has mirrored. The
// v0.2.0 shape adds apiVersion and nests transport state under a
// `mirror` block; v0.1.0 flat files are read transparently.
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

const APIVersion = "mhelm.io/v1alpha1"

type File struct {
	APIVersion string      `json:"apiVersion"`
	Mirror     MirrorBlock `json:"mirror"`
	Wrap       *WrapBlock  `json:"wrap,omitempty"`
	Drift      *Drift      `json:"drift,omitempty"`
}

// MirrorBlock is the transport-phase output: what was pulled, what was
// pushed, every image digest in between, and a tool/version/timestamp
// stamp that identifies the producer.
type MirrorBlock struct {
	Chart      Chart      `json:"chart"`
	Upstream   Upstream   `json:"upstream"`
	Downstream Downstream `json:"downstream"`
	Images     []Image    `json:"images,omitempty"`
	Tool       string     `json:"tool"`
	Version    string     `json:"version"`
	Timestamp  time.Time  `json:"timestamp"`
}

// WrapBlock records the output of `mhelm wrap` — the wrapper chart's
// identity, the mirrored upstream it depends on, and the canonical
// repository paths it would deploy to the cluster.
type WrapBlock struct {
	Chart          WrapChart `json:"chart"`
	DependsOn      WrapDep   `json:"dependsOn"`
	DeployedImages []string  `json:"deployedImages,omitempty"`
	Tool           string    `json:"tool"`
	Version        string    `json:"version"`
	Timestamp      time.Time `json:"timestamp"`
}

// WrapChart identifies the wrapper artifact mhelm authored + pushed.
type WrapChart struct {
	Name               string `json:"name"`
	Version            string `json:"version"`
	Ref                string `json:"ref"`
	OCIManifestDigest  string `json:"ociManifestDigest,omitempty"`
	ChartContentDigest string `json:"chartContentDigest,omitempty"`
}

// WrapDep is the mirrored upstream the wrapper depends on. Distinct
// from MirrorBlock.Downstream so verifiers can resolve the dependency
// graph from the lockfile alone.
type WrapDep struct {
	Ref               string `json:"ref"`
	OCIManifestDigest string `json:"ociManifestDigest,omitempty"`
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
	Source           string       `json:"source,omitempty"`        // one of the Source* constants
	DiscoveredVia    string       `json:"discoveredVia,omitempty"` // one of the DiscoveredVia* constants
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
	SourceManual     = "manual"     // chart.json mirror.extraImages[]
	SourceEnv        = "env"        // containers[].env[].value (regex + validated)
	SourceConfigMap  = "configmap"  // any ConfigMap data value (regex + validated)
	SourceCRDSpec    = "crd-spec"   // non-builtin kind walked (regex + validated)
)

// DiscoveredVia labels record *which configured input surface* caused an
// image to be mirrored: the chart defaults, the user's mirror.discoveryValues,
// or the explicit mirror.extraImages list. This is the audit answer to
// "why is this image in my mirror?" — orthogonal to Source (the extractor).
const (
	DiscoveredViaDefaults        = "defaults"
	DiscoveredViaDiscoveryValues = "discoveryValues"
	DiscoveredViaExtraImages     = "extraImages"
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
	// path (cert chain + Rekor entry) succeeded, OR when the image is
	// explicitly allowlisted via mirror.verify.allowUnsigned (in which
	// case Allowlisted is also true and Type == "allowlisted").
	Verified bool `json:"verified"`
	// Type: "cosign-keyless" | "cosign-key" | "none" | "unreachable" |
	// "error" | "allowlisted". "unreachable" means verification could not
	// complete (trust roots or registry unreachable) — distinct from
	// "none" (verification ran; genuinely no signature).
	Type string `json:"type"`
	// Subject / Issuer: the OIDC identity from the Fulcio cert (keyless).
	Subject string `json:"subject,omitempty"`
	Issuer  string `json:"issuer,omitempty"`
	// RekorLogIndex: transparency log entry index for audit.
	RekorLogIndex int64 `json:"rekorLogIndex,omitempty"`
	// Error: non-empty when Type == "error" or "unreachable" (carries the
	// underlying cause for triage).
	Error string `json:"error,omitempty"`
	// Allowlisted is true when the image matched mirror.verify.allowUnsigned.
	// The original signature outcome (if any was attempted) is dropped —
	// the allowlist is the supply-chain record.
	Allowlisted bool `json:"allowlisted,omitempty"`
}

type Downstream struct {
	Ref               string `json:"ref"`
	OCIManifestDigest string `json:"ociManifestDigest"`
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

// Marshal renders the canonical on-disk bytes for f (apiVersion defaulted,
// 2-space indent, trailing newline). Deterministic — Write delegates to it
// so `discover --check` can byte-compare without reimplementing the format.
func Marshal(f File) ([]byte, error) {
	if f.APIVersion == "" {
		f.APIVersion = APIVersion
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func Write(path string, f File) error {
	data, err := Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Read loads an existing chart-lock.json, transparently migrating
// v0.1.0 flat-shape files into the v0.2.0 nested shape. Returns
// os.ErrNotExist when the file does not exist — callers may use
// errors.Is to treat that as "fresh start".
func Read(path string) (File, error) {
	var f File
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}

	var head struct {
		APIVersion string `json:"apiVersion"`
	}
	if jerr := json.Unmarshal(b, &head); jerr != nil {
		return f, jerr
	}
	if head.APIVersion == APIVersion {
		if err := json.Unmarshal(b, &f); err != nil {
			return f, err
		}
		return f, nil
	}
	// v0.1.0 flat shape: schemaVersion=1, top-level chart/upstream/downstream/images.
	var legacy v01Lockfile
	if err := json.Unmarshal(b, &legacy); err != nil {
		return f, err
	}
	return legacy.migrate(), nil
}

// v01Lockfile is the flat v0.1.0 chart-lock.json shape, retained only
// for the migration path inside Read.
type v01Lockfile struct {
	SchemaVersion int        `json:"schemaVersion"`
	Chart         Chart      `json:"chart"`
	Upstream      Upstream   `json:"upstream"`
	Downstream    Downstream `json:"downstream"`
	Images        []Image    `json:"images,omitempty"`
	Mirror        struct {
		Tool      string    `json:"tool"`
		Version   string    `json:"version"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"mirror"`
	Drift *Drift `json:"drift,omitempty"`
}

func (v v01Lockfile) migrate() File {
	return File{
		APIVersion: APIVersion,
		Mirror: MirrorBlock{
			Chart:      v.Chart,
			Upstream:   v.Upstream,
			Downstream: v.Downstream,
			Images:     v.Images,
			Tool:       v.Mirror.Tool,
			Version:    v.Mirror.Version,
			Timestamp:  v.Mirror.Timestamp,
		},
		Drift: v.Drift,
	}
}

// IsNotExist reports whether err signals a missing lockfile (sugar for
// errors.Is(err, os.ErrNotExist)).
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
