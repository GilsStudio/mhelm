// Package wrapfp computes the wrap inputs fingerprint — a stable hash
// over every input that determines the wrapper chart's bytes.
//
// Why this exists: `mhelm wrap` writes lockfile.wrap from
// wrap.valuesFiles + wrap.extraManifests, but wrap.version is a *manual*
// bump. `mhelm discover --check` (the PR gate) only catches
// discover-stage drift (the image set / digests). A wrap-input change
// that doesn't move the image set — a helm/values.yml toggle, an
// extra-manifest edit — used to leave the published wrapper stale and
// undetected. The fingerprint makes that impossible by construction:
// it is the anchor `mhelm wrap`'s fail-closed check and
// `mhelm discover --check`'s comparison both pin to.
//
// The digest is deterministic and portable: file *contents* are hashed
// (not paths' absolute form), the declared list order is preserved, and
// chart.json is normalized through the chartfile struct so whitespace /
// key-order churn never changes the fingerprint.
package wrapfp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

// PriorState classifies the committed wrap block against a freshly
// recomputed fingerprint. It is the pure decision the `mhelm wrap`
// fail-closed gate switches on (extracted so the matrix is unit-testable
// without the network/registry side of wrap.Run).
type PriorState int

const (
	// FirstWrap: no wrap block recorded yet — nothing to compare.
	FirstWrap PriorState = iota
	// PreV07: a wrap block exists but carries no inputsDigest (lock
	// written before v0.7). Soft-land: recompute + warn, never hard-fail.
	PreV07
	// InSync: recomputed fingerprint equals the committed one — an
	// idempotent re-wrap.
	InSync
	// VersionBumped: wrap.version differs from the published one — a new
	// immutable release; the input change is expected and allowed.
	VersionBumped
	// VersionReuse: same wrap.version, different inputs — the footgun.
	// Fail closed unless the operator passes --allow-version-reuse.
	VersionReuse
)

// ClassifyPrior decides how a recomputed fingerprint fp relates to the
// committed wrap block. wrapVersion is the version `mhelm wrap` resolved
// for this run (cf.Wrap.Version, or the mirrored chart version when
// empty).
func ClassifyPrior(prior *lockfile.WrapBlock, wrapVersion, fp string) PriorState {
	switch {
	case prior == nil:
		return FirstWrap
	case prior.InputsDigest == "":
		return PreV07
	case prior.InputsDigest == fp:
		return InSync
	case prior.Chart.Version != wrapVersion:
		return VersionBumped
	default:
		return VersionReuse
	}
}

type fileBlob struct {
	// Path is the declared (chart.json-relative) path — recorded so a
	// rename is itself an input change, while staying machine-portable.
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// payload is marshaled to canonical JSON and hashed. Struct field order
// is fixed by Go, every slice preserves chart.json declaration order,
// and there are no maps — so the serialization is deterministic.
type payload struct {
	SchemaVersion       string          `json:"schemaVersion"`
	UpstreamChartDigest string          `json:"upstreamChartDigest"`
	ChartJSON           json.RawMessage `json:"chartJSON"`
	DiscoveryValues     []fileBlob      `json:"discoveryValues"`
	WrapValuesFiles     []fileBlob      `json:"wrapValuesFiles"`
	WrapExtraManifests  []fileBlob      `json:"wrapExtraManifests"`
}

// Compute returns "sha256:<hex>" over the canonical wrap input set:
// normalized chart.json, the resolved upstream chart digest, the
// lock-schema version, and the content of every discoveryValues /
// wrap.valuesFiles / wrap.extraManifests file (resolved relative to
// baseDir). A missing input file is an error — fingerprinting over a
// hole would defeat the guarantee.
func Compute(cf chartfile.File, baseDir, upstreamChartDigest, schemaVersion string) (string, error) {
	norm, err := json.Marshal(cf)
	if err != nil {
		return "", fmt.Errorf("normalize chart.json: %w", err)
	}
	p := payload{
		SchemaVersion:       schemaVersion,
		UpstreamChartDigest: upstreamChartDigest,
		ChartJSON:           norm,
	}
	if p.DiscoveryValues, err = hashFiles(cf.DiscoveryValuesEffective(), baseDir); err != nil {
		return "", err
	}
	if cf.Wrap != nil {
		if p.WrapValuesFiles, err = hashFiles(cf.Wrap.ValuesFiles, baseDir); err != nil {
			return "", err
		}
		if p.WrapExtraManifests, err = hashFiles(cf.Wrap.ExtraManifests, baseDir); err != nil {
			return "", err
		}
	}

	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal fingerprint payload: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// hashFiles returns a content hash per path, preserving order. A nil
// input yields a non-nil empty slice so the JSON is a stable `[]`
// (never `null`) regardless of how chart.json omitted the field.
func hashFiles(paths []string, baseDir string) ([]fileBlob, error) {
	out := make([]fileBlob, 0, len(paths))
	for _, rel := range paths {
		p := rel
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("fingerprint: read %s: %w", p, err)
		}
		sum := sha256.Sum256(b)
		out = append(out, fileBlob{Path: rel, SHA256: hex.EncodeToString(sum[:])})
	}
	return out, nil
}
