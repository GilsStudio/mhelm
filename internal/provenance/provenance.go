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
	// Policy records the supply-chain waivers in effect for this mirror
	// run — vuln-allowlist + allowUnsigned exemptions. Surfaced in the
	// attestation so reviewers can audit *what was waived* directly from
	// the signed predicate, not from CI logs.
	Policy       *Policy       `json:"policy,omitempty"`
	BuildContext *BuildContext `json:"buildContext,omitempty"`
}

// Policy mirrors chart.json#mirror.{verify.allowUnsigned, vulnPolicy}.
type Policy struct {
	VulnFailOn         string       `json:"vulnFailOn,omitempty"`
	VulnAllowlist      []VulnWaiver `json:"vulnAllowlist,omitempty"`
	AllowUnsignedRepos []string     `json:"allowUnsignedRepos,omitempty"`
}

// VulnWaiver is the in-attestation copy of chartfile.VulnWaiver
// (decoupled to keep the predicate JSON shape independent).
type VulnWaiver struct {
	CVE     string `json:"cve"`
	Expires string `json:"expires"`
	Reason  string `json:"reason"`
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
			Type:               cf.Mirror.Upstream.Type,
			URL:                cf.Mirror.Upstream.URL,
			ChartName:          lf.Mirror.Chart.Name,
			ChartVersion:       lf.Mirror.Chart.Version,
			ChartContentDigest: lf.Mirror.Upstream.ChartContentDigest,
		},
		Downstream: Downstream{
			RegistryPrefix: strings.TrimPrefix(cf.Mirror.Downstream.URL, "oci://"),
			Chart: Artifact{
				Ref:            lf.Mirror.Downstream.Ref,
				ManifestDigest: lf.Mirror.Downstream.OCIManifestDigest,
			},
		},
		Policy: buildPolicy(cf),
	}

	for _, img := range lf.Mirror.Images {
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

// buildPolicy returns the Policy block iff any waiver/exemption is
// configured. Empty policy is omitted so unwaived mirror runs stay terse.
func buildPolicy(cf chartfile.File) *Policy {
	hasVuln := cf.Mirror.VulnPolicy != nil && (cf.Mirror.VulnPolicy.FailOn != "" || len(cf.Mirror.VulnPolicy.Allowlist) > 0)
	hasAllow := len(cf.Mirror.Verify.AllowUnsigned) > 0
	if !hasVuln && !hasAllow {
		return nil
	}
	p := &Policy{AllowUnsignedRepos: cf.Mirror.Verify.AllowUnsigned}
	if cf.Mirror.VulnPolicy != nil {
		p.VulnFailOn = cf.Mirror.VulnPolicy.FailOnEffective()
		for _, w := range cf.Mirror.VulnPolicy.Allowlist {
			p.VulnAllowlist = append(p.VulnAllowlist, VulnWaiver{
				CVE:     w.CVE,
				Expires: w.Expires,
				Reason:  w.Reason,
			})
		}
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
