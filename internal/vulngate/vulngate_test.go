package vulngate

import (
	"testing"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
)

func reportFrom(matches ...struct{ id, sev string }) Report {
	var r Report
	for _, m := range matches {
		r.Scanner.Result.Matches = append(r.Scanner.Result.Matches, Match{
			Vulnerability: struct {
				ID       string `json:"id"`
				Severity string `json:"severity"`
			}{ID: m.id, Severity: m.sev},
		})
	}
	return r
}

func TestEvaluate_DefaultCriticalGatesCritical(t *testing.T) {
	now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	r := reportFrom(struct{ id, sev string }{"CVE-1", "Critical"})
	res := Evaluate(nil, r, "img:1", now)
	if res.Pass {
		t.Fatal("expected fail on Critical with default policy")
	}
	if len(res.Failures) != 1 || res.Failures[0].CVE != "CVE-1" || res.Failures[0].Reason != "at-or-above-threshold" {
		t.Errorf("failures = %+v", res.Failures)
	}
}

func TestEvaluate_HighBelowCriticalDefaultPasses(t *testing.T) {
	now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	r := reportFrom(struct{ id, sev string }{"CVE-2", "High"})
	res := Evaluate(nil, r, "img:1", now)
	if !res.Pass {
		t.Fatalf("expected pass on High with default critical threshold, got %+v", res)
	}
	if res.BelowThreshold != 1 {
		t.Errorf("BelowThreshold = %d, want 1", res.BelowThreshold)
	}
}

func TestEvaluate_FailOnHighGatesHigh(t *testing.T) {
	now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	policy := &chartfile.VulnPolicy{FailOn: chartfile.FailOnHigh}
	r := reportFrom(struct{ id, sev string }{"CVE-2", "High"})
	res := Evaluate(policy, r, "img:1", now)
	if res.Pass {
		t.Fatal("expected fail on High when failOn=high")
	}
}

func TestEvaluate_AllowlistedFutureExpiresPasses(t *testing.T) {
	now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	policy := &chartfile.VulnPolicy{
		Allowlist: []chartfile.VulnWaiver{
			{CVE: "CVE-1", Expires: "2030-01-01", Reason: "tracked upstream"},
		},
	}
	r := reportFrom(struct{ id, sev string }{"CVE-1", "Critical"})
	res := Evaluate(policy, r, "img:1", now)
	if !res.Pass {
		t.Fatalf("expected pass on allowlisted CVE-1, got %+v", res)
	}
	if len(res.Waived) != 1 || res.Waived[0].CVE != "CVE-1" {
		t.Errorf("waived = %+v", res.Waived)
	}
}

func TestEvaluate_AllowlistedPastExpiresHardFails(t *testing.T) {
	now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	policy := &chartfile.VulnPolicy{
		Allowlist: []chartfile.VulnWaiver{
			{CVE: "CVE-1", Expires: "2024-01-01", Reason: "tracked upstream"},
		},
	}
	r := reportFrom(struct{ id, sev string }{"CVE-1", "Critical"})
	res := Evaluate(policy, r, "img:1", now)
	if res.Pass {
		t.Fatal("expected hard fail on expired waiver")
	}
	if len(res.Failures) != 1 || res.Failures[0].Reason != "waiver-expired" {
		t.Errorf("failures = %+v", res.Failures)
	}
}

func TestEvaluate_FailOnNeverPassesEverything(t *testing.T) {
	now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	policy := &chartfile.VulnPolicy{FailOn: chartfile.FailOnNever}
	r := reportFrom(
		struct{ id, sev string }{"CVE-1", "Critical"},
		struct{ id, sev string }{"CVE-2", "High"},
	)
	res := Evaluate(policy, r, "img:1", now)
	if !res.Pass {
		t.Fatalf("expected pass with failOn=never, got %+v", res)
	}
	if res.BelowThreshold != 2 {
		t.Errorf("BelowThreshold = %d, want 2", res.BelowThreshold)
	}
}

func TestEvaluate_CVECaseInsensitive(t *testing.T) {
	now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	policy := &chartfile.VulnPolicy{
		Allowlist: []chartfile.VulnWaiver{
			{CVE: "cve-2024-1", Expires: "2030-01-01", Reason: "x"},
		},
	}
	r := reportFrom(struct{ id, sev string }{"CVE-2024-1", "Critical"})
	res := Evaluate(policy, r, "img:1", now)
	if !res.Pass {
		t.Fatalf("expected case-insensitive CVE match to waive, got %+v", res)
	}
}
