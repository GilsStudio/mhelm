// Package slsa builds a SLSA v1.0 build provenance predicate
// (https://slsa.dev/spec/v1.0/provenance) describing the mhelm mirror
// operation. The mhelm GitHub Action passes the resulting JSON to cosign:
//
//	cosign attest --predicate slsa-provenance.json \
//	    --type slsaprovenance1 \
//	    <downstream-ref>@<digest>
//
// One predicate file is generated per mirror run and reused across all
// downstream artifacts (cosign attest sets the per-artifact subject from
// the ref).
package slsa

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
)

const (
	PredicateType = "https://slsa.dev/provenance/v1"
	BuildType     = "https://mhelm.dev/MirrorBuild/v1"
)

// now is the clock used for Metadata.StartedOn. Overridden by tests for
// deterministic golden output.
var now = func() time.Time { return time.Now().UTC() }

type Predicate struct {
	BuildDefinition BuildDefinition `json:"buildDefinition"`
	RunDetails      RunDetails      `json:"runDetails"`
}

type BuildDefinition struct {
	BuildType            string               `json:"buildType"`
	ExternalParameters   ExternalParameters   `json:"externalParameters"`
	InternalParameters   map[string]string    `json:"internalParameters,omitempty"`
	ResolvedDependencies []ResourceDescriptor `json:"resolvedDependencies,omitempty"`
}

type ExternalParameters struct {
	Source   *ResourceDescriptor `json:"source,omitempty"`
	ChartDir string              `json:"chartDir"`
}

type ResourceDescriptor struct {
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
	Name   string            `json:"name,omitempty"`
}

type RunDetails struct {
	Builder  Builder  `json:"builder"`
	Metadata Metadata `json:"metadata"`
}

type Builder struct {
	ID      string            `json:"id"`
	Version map[string]string `json:"version,omitempty"`
}

type Metadata struct {
	InvocationID string    `json:"invocationId,omitempty"`
	StartedOn    time.Time `json:"startedOn"`
}

// Build assembles the SLSA v1 provenance predicate from chart.json,
// chart-lock.json, and the GitHub Actions environment. dir is the chart
// directory relative to the consumer repo's checkout root, recorded so
// reviewers can locate the originating spec.
func Build(cf chartfile.File, lf lockfile.File, dir, mhelmVersion string) Predicate {
	p := Predicate{
		BuildDefinition: BuildDefinition{
			BuildType: BuildType,
			ExternalParameters: ExternalParameters{
				ChartDir: dir,
				Source:   sourceDescriptor(),
			},
		},
		RunDetails: RunDetails{
			Builder: Builder{
				ID:      builderID(),
				Version: map[string]string{"mhelm": mhelmVersion},
			},
			Metadata: Metadata{
				InvocationID: invocationID(),
				StartedOn:    now(),
			},
		},
	}

	if lf.Upstream.ChartContentDigest != "" {
		p.BuildDefinition.ResolvedDependencies = append(
			p.BuildDefinition.ResolvedDependencies,
			ResourceDescriptor{
				URI:    cf.Upstream.URL,
				Name:   lf.Chart.Name + "@" + lf.Chart.Version,
				Digest: digestMap(lf.Upstream.ChartContentDigest),
			},
		)
	}
	for _, img := range lf.Images {
		if img.Digest == "" {
			continue
		}
		p.BuildDefinition.ResolvedDependencies = append(
			p.BuildDefinition.ResolvedDependencies,
			ResourceDescriptor{
				URI:    img.Ref,
				Digest: digestMap(img.Digest),
			},
		)
	}

	return p
}

// builderID returns the canonical SLSA-recommended identifier for a GHA
// workflow: <server>/<owner>/<repo>/.github/workflows/<file>@<ref>.
// Falls back to an empty string when not running in GHA (e.g. local dev).
func builderID() string {
	server := os.Getenv("GITHUB_SERVER_URL")
	workflowRef := os.Getenv("GITHUB_WORKFLOW_REF")
	if server == "" || workflowRef == "" {
		return ""
	}
	return server + "/" + workflowRef
}

func invocationID() string {
	runID := os.Getenv("GITHUB_RUN_ID")
	if runID == "" {
		return ""
	}
	if attempt := os.Getenv("GITHUB_RUN_ATTEMPT"); attempt != "" {
		return runID + "-" + attempt
	}
	return runID
}

// sourceDescriptor reports the git source the build was run against, when
// running in GHA. The digest's `gitCommit` field follows the SLSA v1
// convention for git source commits.
func sourceDescriptor() *ResourceDescriptor {
	server := os.Getenv("GITHUB_SERVER_URL")
	repo := os.Getenv("GITHUB_REPOSITORY")
	sha := os.Getenv("GITHUB_SHA")
	ref := os.Getenv("GITHUB_REF")
	if server == "" || repo == "" {
		return nil
	}
	uri := "git+" + server + "/" + repo
	if ref != "" {
		uri += "@" + ref
	}
	var digest map[string]string
	if sha != "" {
		digest = map[string]string{"gitCommit": sha}
	}
	return &ResourceDescriptor{URI: uri, Digest: digest}
}

func digestMap(digest string) map[string]string {
	alg, hex := "sha256", digest
	if i := strings.Index(digest, ":"); i >= 0 {
		alg = digest[:i]
		hex = digest[i+1:]
	}
	return map[string]string{alg: hex}
}

func Write(path string, p Predicate) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
