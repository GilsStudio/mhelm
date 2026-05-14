// Package provenance builds the custom `mhelm.dev/MirrorProvenance/v1`
// in-toto predicate. The mhelm GitHub Action passes the resulting JSON to
// cosign:
//
//	cosign attest --predicate mirror-provenance.json \
//	    --type https://mhelm.dev/MirrorProvenance/v1 \
//	    <downstream-ref>@<digest>
//
// Cosign wraps the predicate body in a DSSE envelope and signs it, so we
// only need to produce the predicate fields here. No in-toto Go library
// dependency required.
package provenance

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

const PredicateType = "https://mhelm.dev/MirrorProvenance/v1"

// now is the clock used for RanAt. Overridden by tests for deterministic
// golden output.
var now = func() time.Time { return time.Now().UTC() }

type Predicate struct {
	Type               string           `json:"_type"`
	Tool               Tool             `json:"tool"`
	RanAt              time.Time        `json:"ranAt"`
	Upstream           Upstream         `json:"upstream"`
	Downstream         Downstream       `json:"downstream"`
	UpstreamSignatures []SignatureEntry `json:"upstreamSignatures,omitempty"`
	BuildContext       *BuildContext    `json:"buildContext,omitempty"`
}

type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Upstream struct {
	Type               string `json:"type"`
	URL                string `json:"url"`
	ChartName          string `json:"chartName"`
	ChartVersion       string `json:"chartVersion"`
	ChartContentDigest string `json:"chartContentDigest"`
}

type Downstream struct {
	RegistryPrefix string          `json:"registryPrefix"`
	Chart          Artifact        `json:"chart"`
	Images         []ImageArtifact `json:"images,omitempty"`
}

type Artifact struct {
	Ref            string `json:"ref"`
	ManifestDigest string `json:"manifestDigest"`
}

type ImageArtifact struct {
	UpstreamRef    string `json:"upstreamRef"`
	DownstreamRef  string `json:"downstreamRef"`
	ManifestDigest string `json:"manifestDigest"`
}

// SignatureEntry records one image's upstream-signature verification
// result so consumers of MirrorProvenance can audit not just *what*
// mhelm mirrored but *who signed it upstream*.
type SignatureEntry struct {
	Ref           string `json:"ref"`
	Verified      bool   `json:"verified"`
	Type          string `json:"type"`
	Subject       string `json:"subject,omitempty"`
	Issuer        string `json:"issuer,omitempty"`
	RekorLogIndex int64  `json:"rekorLogIndex,omitempty"`
}

// BuildContext is populated from the GitHub Actions env when present.
// Omitted entirely when mhelm runs outside CI.
type BuildContext struct {
	Repository string `json:"repository,omitempty"`
	Workflow   string `json:"workflow,omitempty"`
	RunID      string `json:"runId,omitempty"`
	RunAttempt string `json:"runAttempt,omitempty"`
	SHA        string `json:"sha,omitempty"`
	RefName    string `json:"refName,omitempty"`
}

// Build assembles the predicate from the user's chart.json and the
// generated chart-lock.json. mhelmVersion is the running CLI version,
// captured so consumers know which mhelm produced the attestation.
func Build(cf chartfile.File, lf lockfile.File, mhelmVersion string) Predicate {
	p := Predicate{
		Type:  PredicateType,
		Tool:  Tool{Name: "mhelm", Version: mhelmVersion},
		RanAt: now(),
		Upstream: Upstream{
			Type:               cf.Upstream.Type,
			URL:                cf.Upstream.URL,
			ChartName:          lf.Chart.Name,
			ChartVersion:       lf.Chart.Version,
			ChartContentDigest: lf.Upstream.ChartContentDigest,
		},
		Downstream: Downstream{
			RegistryPrefix: strings.TrimPrefix(cf.Downstream.URL, "oci://"),
			Chart: Artifact{
				Ref:            lf.Downstream.Ref,
				ManifestDigest: lf.Downstream.OCIManifestDigest,
			},
		},
	}

	for _, img := range lf.Images {
		if img.DownstreamRef != "" {
			p.Downstream.Images = append(p.Downstream.Images, ImageArtifact{
				UpstreamRef:    img.Ref,
				DownstreamRef:  img.DownstreamRef,
				ManifestDigest: img.DownstreamDigest,
			})
		}
		if img.Signature != nil {
			p.UpstreamSignatures = append(p.UpstreamSignatures, SignatureEntry{
				Ref:           img.Ref,
				Verified:      img.Signature.Verified,
				Type:          img.Signature.Type,
				Subject:       img.Signature.Subject,
				Issuer:        img.Signature.Issuer,
				RekorLogIndex: img.Signature.RekorLogIndex,
			})
		}
	}

	if ctx := buildContextFromEnv(); ctx != nil {
		p.BuildContext = ctx
	}

	return p
}

func buildContextFromEnv() *BuildContext {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return nil
	}
	return &BuildContext{
		Repository: repo,
		Workflow:   os.Getenv("GITHUB_WORKFLOW"),
		RunID:      os.Getenv("GITHUB_RUN_ID"),
		RunAttempt: os.Getenv("GITHUB_RUN_ATTEMPT"),
		SHA:        os.Getenv("GITHUB_SHA"),
		RefName:    os.Getenv("GITHUB_REF_NAME"),
	}
}

func Write(path string, p Predicate) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
