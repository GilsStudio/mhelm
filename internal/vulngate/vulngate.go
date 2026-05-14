// Package vulngate evaluates a grype `cosign-vuln`-format JSON file
// against the vuln-policy declared in chart.json#mirror.vulnPolicy.
// Returns a structured Result; callers decide how to surface it.
//
// Used by the `mhelm vuln-gate` subcommand which the mhelm GitHub
// Action invokes once per image after grype completes, before the
// cosign vuln attestation is attached.
package vulngate

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
)

// Report is the decoded grype cosign-vuln/v1 predicate shape. Only the
// fields needed for gating are unmarshalled; the rest pass through.
type Report struct {
	Scanner struct {
		Result struct {
			Matches []Match `json:"matches"`
		} `json:"result"`
	} `json:"scanner"`
}

// Match is one vulnerability finding.
type Match struct {
	Vulnerability struct {
		ID       string `json:"id"`
		Severity string `json:"severity"`
	} `json:"vulnerability"`
}

// Result is the policy decision for one vuln report.
type Result struct {
	Image          string
	Pass           bool
	Failures       []Failure
	Waived         []Waiver
	BelowThreshold int
}

// Failure is a vulnerability that gates the mirror.
type Failure struct {
	CVE      string
	Severity string
	Reason   string // "at-or-above-threshold" | "waiver-expired"
}

// Waiver is a finding that was suppressed by an allowlist entry.
type Waiver struct {
	CVE     string
	Expires string
	Reason  string
}

// Evaluate applies policy to report. now is injected so tests can pin
// the clock; production callers pass time.Now().UTC().
func Evaluate(policy *chartfile.VulnPolicy, report Report, image string, now time.Time) Result {
	res := Result{Image: image, Pass: true}

	failOn := chartfile.FailOnCritical
	allowlist := map[string]chartfile.VulnWaiver{}
	if policy != nil {
		failOn = policy.FailOnEffective()
		for _, w := range policy.Allowlist {
			allowlist[strings.ToUpper(w.CVE)] = w
		}
	}
	threshold := severityRank(failOn)

	for _, m := range report.Scanner.Result.Matches {
		cve := strings.ToUpper(m.Vulnerability.ID)
		sev := m.Vulnerability.Severity
		rank := severityRank(sev)

		if w, ok := allowlist[cve]; ok {
			expires, err := w.ExpiresTime()
			if err != nil || expires.Before(now) {
				res.Pass = false
				res.Failures = append(res.Failures, Failure{
					CVE:      cve,
					Severity: sev,
					Reason:   "waiver-expired",
				})
				continue
			}
			res.Waived = append(res.Waived, Waiver{
				CVE:     cve,
				Expires: w.Expires,
				Reason:  w.Reason,
			})
			continue
		}

		// failOn=never disables threshold gating; below-threshold findings pass.
		if threshold == severityNever || rank < threshold {
			res.BelowThreshold++
			continue
		}
		res.Pass = false
		res.Failures = append(res.Failures, Failure{
			CVE:      cve,
			Severity: sev,
			Reason:   "at-or-above-threshold",
		})
	}
	return res
}

// LoadReport reads a grype cosign-vuln JSON file.
func LoadReport(path string) (Report, error) {
	var r Report
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("parse %s: %w", path, err)
	}
	return r, nil
}

// Severity ranks. severityNever is the sentinel for failOn="never" — it
// can never be matched by a real finding because grype severities are
// always one of negligible/low/medium/high/critical/unknown.
const (
	severityNegligible = 1
	severityLow        = 2
	severityMedium     = 3
	severityHigh       = 4
	severityCritical   = 5
	severityNever      = 1 << 30
)

// severityRank maps grype/cosign severity strings to ranks. Unknown
// severities sort below negligible (rank 0) so they never trip
// threshold gating — operators see them in the report but waiver
// policy is the right tool to address them.
func severityRank(s string) int {
	switch strings.ToLower(s) {
	case chartfile.FailOnNever:
		return severityNever
	case "critical":
		return severityCritical
	case "high":
		return severityHigh
	case "medium":
		return severityMedium
	case "low":
		return severityLow
	case "negligible":
		return severityNegligible
	default:
		return 0
	}
}
