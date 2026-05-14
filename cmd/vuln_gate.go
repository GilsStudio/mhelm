package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/vulngate"
	"github.com/spf13/cobra"
)

var (
	vulnGateVulnFile string
	vulnGateImage    string
)

var vulnGateCmd = &cobra.Command{
	Use:          "vuln-gate [dir]",
	SilenceUsage: true,
	Short:        "Apply chart.json#mirror.vulnPolicy to a grype cosign-vuln report for one image",
	Long: `Read <dir>/chart.json's mirror.vulnPolicy (failOn threshold + CVE allowlist)
and apply it to the grype cosign-vuln JSON at --vuln-file for the image at
--image. Exits non-zero when:

  - any finding at or above the configured failOn severity is not
    suppressed by an allowlist entry, OR
  - any allowlist entry that matched a finding has an expired 'expires'
    date — forces refresh.

Used by the mhelm GitHub Action per image, between grype and cosign
attest, so a policy violation fails the run before the bad image gets
a vuln attestation.

Pure read — no network, no writes.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		if vulnGateVulnFile == "" {
			return fmt.Errorf("--vuln-file is required")
		}
		if vulnGateImage == "" {
			return fmt.Errorf("--image is required")
		}
		cf, err := chartfile.Load(filepath.Join(dir, chartFileName))
		if err != nil {
			return err
		}
		report, err := vulngate.LoadReport(vulnGateVulnFile)
		if err != nil {
			return err
		}
		res := vulngate.Evaluate(cf.Mirror.VulnPolicy, report, vulnGateImage, time.Now().UTC())

		fmt.Fprintf(cmd.OutOrStdout(), "vuln-gate %s — failOn=%s, below=%d, waived=%d, failures=%d\n",
			vulnGateImage, cf.Mirror.VulnPolicy.FailOnEffective(),
			res.BelowThreshold, len(res.Waived), len(res.Failures))
		for _, w := range res.Waived {
			fmt.Fprintf(cmd.OutOrStdout(), "  [waived]  %s (expires %s) — %s\n", w.CVE, w.Expires, w.Reason)
		}
		for _, f := range res.Failures {
			fmt.Fprintf(cmd.OutOrStdout(), "  [FAIL]    %s (%s) — %s\n", f.CVE, f.Severity, f.Reason)
		}
		if !res.Pass {
			return fmt.Errorf("vuln-gate failed: %d violation(s) for %s", len(res.Failures), vulnGateImage)
		}
		return nil
	},
}

func init() {
	vulnGateCmd.Flags().StringVar(&vulnGateVulnFile, "vuln-file", "", "path to grype cosign-vuln JSON for the image")
	vulnGateCmd.Flags().StringVar(&vulnGateImage, "image", "", "image ref the report describes (for log + audit)")
}
