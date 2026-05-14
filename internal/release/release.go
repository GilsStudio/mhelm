// Package release turns chart.json#release + chart-lock.json into a
// `helm upgrade --install` invocation. It does NOT execute helm; the
// boundary between mhelm and helm stays sharp.
//
// When chart-lock.json carries a `wrap` block, the wrapper is the
// install target (highest-fidelity artifact). Otherwise the bare
// mirrored chart is used. There is no `release.source` override
// knob — adopters who want the bare mirror simply don't configure
// `wrap`.
package release

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

// Source describes which artifact in the lockfile the install plan
// targets.
type Source string

const (
	SourceWrap   Source = "wrap"
	SourceMirror Source = "mirror"
)

// Plan is the fully-resolved set of inputs for one `helm upgrade
// --install` invocation. Render() produces the bash command.
type Plan struct {
	Source         Source
	ReleaseName    string
	Namespace      string
	OCIRef         string // oci://<registry>/<path>/<name> (no version, no tag)
	Version        string
	ManifestDigest string
	ValuesFiles    []string
}

// ErrNoRelease is returned by Resolve when chart.json has no release
// section. Surface this to the user with a clear remediation hint.
var ErrNoRelease = errors.New("chart.json has no release section — run `mhelm release init` to scaffold one")

// ErrNoArtifact is returned when the lockfile has neither a wrap
// block nor a mirrored chart to install.
var ErrNoArtifact = errors.New("chart-lock.json has no installable artifact — run `mhelm mirror` (and optionally `mhelm wrap`) first")

// Resolve picks the artifact and assembles the Plan.
func Resolve(cf chartfile.File, lf lockfile.File) (Plan, error) {
	if cf.Release == nil {
		return Plan{}, ErrNoRelease
	}
	p := Plan{
		ReleaseName: cf.Release.Name,
		Namespace:   cf.Release.Namespace,
		ValuesFiles: cf.Release.ValuesFiles,
	}
	switch {
	case lf.Wrap != nil && lf.Wrap.Chart.Ref != "":
		p.Source = SourceWrap
		p.OCIRef = stripTag(lf.Wrap.Chart.Ref)
		p.Version = lf.Wrap.Chart.Version
		p.ManifestDigest = lf.Wrap.Chart.OCIManifestDigest
	case lf.Mirror.Downstream.Ref != "":
		p.Source = SourceMirror
		p.OCIRef = stripTag(lf.Mirror.Downstream.Ref)
		p.Version = lf.Mirror.Chart.Version
		p.ManifestDigest = lf.Mirror.Downstream.OCIManifestDigest
	default:
		return Plan{}, ErrNoArtifact
	}
	return p, nil
}

// Render produces the bash-runnable command block, prefixed with one
// comment line per audit fact (source, chart, digest). The command is
// split across lines with backslash continuations for readability;
// bash parses the whole block as a single statement.
func (p Plan) Render() string {
	var b strings.Builder
	fmt.Fprintln(&b, "# mhelm release print-install — auto-generated, safe to pipe to bash")
	fmt.Fprintf(&b, "# source:  %s\n", p.Source)
	fmt.Fprintf(&b, "# chart:   %s:%s\n", p.OCIRef, p.Version)
	if p.ManifestDigest != "" {
		fmt.Fprintf(&b, "# digest:  %s\n", p.ManifestDigest)
	}
	lines := []string{
		fmt.Sprintf("helm upgrade --install %s %s", p.ReleaseName, "oci://"+strings.TrimPrefix(p.OCIRef, "oci://")),
		fmt.Sprintf("--version %s", p.Version),
		fmt.Sprintf("--namespace %s", p.Namespace),
		"--create-namespace",
	}
	for _, vf := range p.ValuesFiles {
		lines = append(lines, fmt.Sprintf("-f %s", vf))
	}
	for i, line := range lines {
		if i < len(lines)-1 {
			fmt.Fprintf(&b, "%s \\\n  ", line)
		} else {
			fmt.Fprintln(&b, line)
		}
	}
	return b.String()
}

// stripTag returns ref with any `:tag` suffix removed. Digest suffixes
// (`@sha256:...`) are also stripped. The result is the bare repository
// path that helm install accepts when paired with `--version`.
func stripTag(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		// Don't mistake a `host:port` for a tag.
		if !strings.Contains(ref[i+1:], "/") {
			ref = ref[:i]
		}
	}
	return ref
}
