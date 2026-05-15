// Package chartfile parses chart.json, the user-edited input spec that
// describes a chart to mirror. v0.2.0 introduces apiVersion +
// mirror/wrap sections; the older flat v0.1.0 shape is accepted on read
// and warned about, never rewritten on disk.
package chartfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

const (
	TypeRepo = "repo"
	TypeOCI  = "oci"

	APIVersion = "mhelm.io/v1alpha1"

	FailOnCritical = "critical"
	FailOnHigh     = "high"
	FailOnMedium   = "medium"
	FailOnNever    = "never"
)

type Endpoint struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// File is the in-memory representation of chart.json. Always carries
// APIVersion = mhelm.io/v1alpha1 after Load; v0.1.0 inputs are
// transparently migrated.
type File struct {
	APIVersion string   `json:"apiVersion"`
	Mirror     Mirror   `json:"mirror"`
	Wrap       *Wrap    `json:"wrap,omitempty"`
	Release    *Release `json:"release,omitempty"`
}

// Mirror owns transport: upstream identity, downstream destination,
// discovery surface, signature policy, vulnerability policy.
type Mirror struct {
	Upstream        Endpoint     `json:"upstream"`
	Downstream      Endpoint     `json:"downstream"`
	DiscoveryValues []string     `json:"discoveryValues,omitempty"`
	ExtraImages     []ExtraImage `json:"extraImages,omitempty"`
	Verify          Verify       `json:"verify,omitempty"`
	VulnPolicy      *VulnPolicy  `json:"vulnPolicy,omitempty"`
}

// Verify is the signature-policy surface.
type Verify struct {
	TrustedIdentities []TrustedIdentity `json:"trustedIdentities,omitempty"`
	// AllowUnsigned lists image repository paths (e.g. "cilium/hubble-ui")
	// for which a missing or unverifiable upstream signature is acceptable.
	// Matched against the canonical repo path of each image ref. Exact
	// match, no globs.
	AllowUnsigned []string `json:"allowUnsigned,omitempty"`
}

// TrustedIdentity describes one allowed signing identity for cosign
// keyless verification. SubjectRegex matches against the Fulcio cert's
// SAN (typically a GitHub Actions workflow URL); Issuer is the OIDC
// issuer URL.
type TrustedIdentity struct {
	SubjectRegex string `json:"subjectRegex"`
	Issuer       string `json:"issuer"`
}

// VulnPolicy gates `mhelm vuln-gate` (which reads grype cosign-vuln
// JSON and applies these rules per image).
type VulnPolicy struct {
	// FailOn is the severity threshold: "critical" | "high" | "medium" | "never".
	// Defaults to "critical" via FailOnEffective() when empty.
	FailOn    string       `json:"failOn,omitempty"`
	Allowlist []VulnWaiver `json:"allowlist,omitempty"`
}

// FailOnEffective returns the configured threshold or the default.
func (p *VulnPolicy) FailOnEffective() string {
	if p == nil || p.FailOn == "" {
		return FailOnCritical
	}
	return p.FailOn
}

// VulnWaiver allowlists a single CVE for a bounded window. Expired
// waivers hard-fail `mhelm vuln-gate` to force refresh.
type VulnWaiver struct {
	CVE     string `json:"cve"`
	Expires string `json:"expires"` // YYYY-MM-DD
	Reason  string `json:"reason"`
}

// ExpiresTime parses the YYYY-MM-DD expires date. Returns the zero
// time and an error when malformed.
func (w VulnWaiver) ExpiresTime() (time.Time, error) {
	return time.Parse("2006-01-02", w.Expires)
}

// ExtraImage is a manual entry for an image automated discovery can't
// find (operator-managed, CRD-embedded, webhook-injected). Reason is
// recorded in MirrorProvenance so the supply-chain audit captures
// *why* a non-discovered image was mirrored.
type ExtraImage struct {
	Ref        string `json:"ref"`
	ValuesPath string `json:"valuesPath,omitempty"`
	// OverridePath, when set, additionally emits the whole pinned ref as
	// a single string at that path (e.g. cilium's `image.override`) so a
	// chart can bypass its own per-cloud suffix concatenation.
	OverridePath string `json:"overridePath,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// Release is the deploy-time ergonomics surface — used by
// `mhelm release print-install` to assemble a `helm upgrade --install`
// invocation against the locked wrapper (or bare mirror chart, when no
// wrap is configured). Single-environment by design; multi-env is
// delegated to helmfile/argo/etc.
type Release struct {
	Name        string   `json:"name,omitempty"`
	Namespace   string   `json:"namespace,omitempty"`
	ValuesFiles []string `json:"valuesFiles,omitempty"`
}

// Wrap is the composition surface — used by `mhelm wrap` to author a
// wrapper Helm chart that depends on the mirrored upstream. Image
// rewrites are NOT configured here in v0.3.0+ — `mhelm wrap` derives
// them automatically from lockfile.mirror.images[].valuesPaths[].
type Wrap struct {
	Name           string   `json:"name,omitempty"`
	Version        string   `json:"version,omitempty"`
	ValuesFiles    []string `json:"valuesFiles,omitempty"`
	ExtraManifests []string `json:"extraManifests,omitempty"`
}

// Load reads chart.json. v0.1.0 flat-shape files are migrated in
// memory and a one-line warning is printed to stderr; the file on
// disk is never rewritten.
func Load(filePath string) (File, error) {
	var f File
	b, err := os.ReadFile(filePath)
	if err != nil {
		return f, err
	}

	var head struct {
		APIVersion string `json:"apiVersion"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return f, fmt.Errorf("parse %s: %w", filePath, err)
	}

	switch head.APIVersion {
	case APIVersion:
		if err := json.Unmarshal(b, &f); err != nil {
			return f, fmt.Errorf("parse %s: %w", filePath, err)
		}
		warnDeprecatedImageOverrides(filePath, b)
		return f, nil
	case "":
		var legacy v01File
		if err := json.Unmarshal(b, &legacy); err != nil {
			return f, fmt.Errorf("parse %s: %w", filePath, err)
		}
		fmt.Fprintf(os.Stderr,
			"warn: %s uses the v0.1.0 schema (no apiVersion). Migrate to %q — see CHANGELOG. (in-memory migration applied; file not modified)\n",
			filePath, APIVersion)
		return legacy.migrate(), nil
	default:
		return f, fmt.Errorf("%s: unsupported apiVersion %q (expected %q or empty for v0.1.0 auto-migrate)",
			filePath, head.APIVersion, APIVersion)
	}
}

// warnDeprecatedImageOverrides surfaces a stderr warning when an
// adopter still carries `wrap.imageOverrides` in their chart.json.
// The field was parsed in v0.2.0 as a schema slot but is removed in
// v0.3.0; rewrites are now auto-derived from
// lockfile.mirror.images[].valuesPaths[].
func warnDeprecatedImageOverrides(filePath string, raw []byte) {
	var sniff struct {
		Wrap struct {
			ImageOverrides map[string]string `json:"imageOverrides"`
		} `json:"wrap"`
	}
	if err := json.Unmarshal(raw, &sniff); err != nil {
		return
	}
	if len(sniff.Wrap.ImageOverrides) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"warn: %s carries `wrap.imageOverrides` — no longer used as of v0.3.0. "+
			"Remove the field; rewrites are derived from lockfile.mirror.images[].valuesPaths[].\n",
		filePath)
}

// v01File is the flat v0.1.0 chart.json shape. Kept only for the
// migration path inside Load.
type v01File struct {
	Upstream          Endpoint          `json:"upstream"`
	Downstream        Endpoint          `json:"downstream"`
	ValuesFiles       []string          `json:"valuesFiles,omitempty"`
	ExtraImages       []ExtraImage      `json:"extraImages,omitempty"`
	TrustedIdentities []TrustedIdentity `json:"trustedIdentities,omitempty"`
}

func (v v01File) migrate() File {
	f := File{
		APIVersion: APIVersion,
		Mirror: Mirror{
			Upstream:    v.Upstream,
			Downstream:  v.Downstream,
			ExtraImages: v.ExtraImages,
			Verify:      Verify{TrustedIdentities: v.TrustedIdentities},
		},
	}
	if len(v.ValuesFiles) > 0 {
		f.Wrap = &Wrap{ValuesFiles: v.ValuesFiles}
	}
	return f
}

func (f File) Validate() error {
	if f.APIVersion != "" && f.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion %q invalid (expected %q)", f.APIVersion, APIVersion)
	}
	up := f.Mirror.Upstream
	switch up.Type {
	case TypeRepo:
		if up.Name == "" {
			return fmt.Errorf("mirror.upstream.name is required when mirror.upstream.type=%q", TypeRepo)
		}
	case TypeOCI:
		if !strings.HasPrefix(up.URL, "oci://") {
			return fmt.Errorf("mirror.upstream.url must start with oci:// when mirror.upstream.type=%q", TypeOCI)
		}
		if up.Name != "" {
			return fmt.Errorf("mirror.upstream.name only applies to mirror.upstream.type=%q — for OCI put the full chart path in mirror.upstream.url (e.g. oci://quay.io/cilium/charts/cilium)", TypeRepo)
		}
	case "":
		return fmt.Errorf("mirror.upstream.type is required (%q or %q)", TypeRepo, TypeOCI)
	default:
		return fmt.Errorf("mirror.upstream.type %q invalid (expected %q or %q)", up.Type, TypeRepo, TypeOCI)
	}
	if up.URL == "" {
		return fmt.Errorf("mirror.upstream.url is required")
	}
	if up.Version == "" {
		return fmt.Errorf("mirror.upstream.version is required")
	}
	if f.Mirror.Downstream.Type != TypeOCI {
		return fmt.Errorf("mirror.downstream.type must be %q (got %q)", TypeOCI, f.Mirror.Downstream.Type)
	}
	if !strings.HasPrefix(f.Mirror.Downstream.URL, "oci://") {
		return fmt.Errorf("mirror.downstream.url must start with oci://")
	}
	for i, e := range f.Mirror.ExtraImages {
		if e.Ref == "" {
			return fmt.Errorf("mirror.extraImages[%d].ref is required", i)
		}
		if e.OverridePath != "" && e.OverridePath == e.ValuesPath {
			return fmt.Errorf("mirror.extraImages[%d]: overridePath must differ from valuesPath", i)
		}
	}
	if f.Wrap != nil {
		if f.Wrap.Name == "" {
			return fmt.Errorf("wrap.name is required when wrap is configured")
		}
		if f.Wrap.Version == "" {
			return fmt.Errorf("wrap.version is required when wrap is configured")
		}
	}
	if f.Release != nil {
		if f.Release.Name == "" {
			return fmt.Errorf("release.name is required when release is configured")
		}
		if f.Release.Namespace == "" {
			return fmt.Errorf("release.namespace is required when release is configured")
		}
	}
	if f.Mirror.VulnPolicy != nil {
		switch f.Mirror.VulnPolicy.FailOn {
		case "", FailOnCritical, FailOnHigh, FailOnMedium, FailOnNever:
		default:
			return fmt.Errorf("mirror.vulnPolicy.failOn %q invalid (expected one of critical/high/medium/never)",
				f.Mirror.VulnPolicy.FailOn)
		}
		for i, w := range f.Mirror.VulnPolicy.Allowlist {
			if w.CVE == "" {
				return fmt.Errorf("mirror.vulnPolicy.allowlist[%d].cve is required", i)
			}
			if w.Expires == "" {
				return fmt.Errorf("mirror.vulnPolicy.allowlist[%d].expires is required (YYYY-MM-DD)", i)
			}
			if _, err := w.ExpiresTime(); err != nil {
				return fmt.Errorf("mirror.vulnPolicy.allowlist[%d].expires %q: %w", i, w.Expires, err)
			}
			if w.Reason == "" {
				return fmt.Errorf("mirror.vulnPolicy.allowlist[%d].reason is required", i)
			}
		}
	}
	return nil
}

// ChartName returns the chart name for the push reference: upstream
// name for repo-type, last path segment of upstream URL for oci-type.
func (f File) ChartName() string {
	up := f.Mirror.Upstream
	if up.Type == TypeRepo {
		return up.Name
	}
	ref := strings.TrimPrefix(up.URL, "oci://")
	return path.Base(ref)
}

// DiscoveryValuesEffective returns the values files the discover
// pipeline should render with. mirror.discoveryValues is the only
// source as of v0.3.0; the v0.2.0 fallback to wrap.valuesFiles is
// gone (wrap.valuesFiles is deployment overlay, not discovery input).
func (f File) DiscoveryValuesEffective() []string {
	return f.Mirror.DiscoveryValues
}
