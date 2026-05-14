package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/drift"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/spf13/cobra"
)

var (
	driftNoUpstreamRotation    bool
	driftNoDownstreamTampering bool
	driftNoNewVersions         bool
	driftExitZero              bool
)

var driftCmd = &cobra.Command{
	Use:           "drift [dir]",
	SilenceUsage:  true,
	SilenceErrors: false,
	Short:         "Detect upstream tag rotation, downstream tampering, and new upstream versions",
	Long: `Read <dir>/chart-lock.json and run three checks against the live state:

  upstream-rotation     HEAD upstream chart + each image at the pinned ref;
                        fail when the digest no longer matches the lockfile.
  downstream-tampered   HEAD downstream chart + each image at the recorded
                        ref; fail when the digest no longer matches.
  new-version-available List upstream tags / index.yaml; report any higher
                        semver than chart.json#upstream.version.

Findings are written to <dir>/chart-lock.json#drift so subsequent runs are
auditable via PR diff. Exits non-zero on any finding (use --exit-zero to
override — useful when the workflow opens a PR rather than failing).

Network reads only — no writes.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		chartPath := filepath.Join(dir, chartFileName)
		lockPath := filepath.Join(dir, lockFileName)

		cf, err := chartfile.Load(chartPath)
		if err != nil {
			return fmt.Errorf("load %s: %w", chartPath, err)
		}
		lf, err := lockfile.Read(lockPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%s not found — run `mhelm mirror` first", lockPath)
			}
			return fmt.Errorf("read %s: %w", lockPath, err)
		}

		opts := drift.Options{
			UpstreamRotation:    !driftNoUpstreamRotation,
			DownstreamTampering: !driftNoDownstreamTampering,
			NewVersions:         !driftNoNewVersions,
		}
		d := drift.Run(cmd.Context(), cf, lf, opts)
		lf.Drift = &d

		if err := lockfile.Write(lockPath, lf); err != nil {
			return fmt.Errorf("write %s: %w", lockPath, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "drift check: %s — %d finding(s)\n", d.CheckedAt.Format("2006-01-02 15:04:05 UTC"), len(d.Findings))
		for _, f := range d.Findings {
			fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s\n", f.Kind, f.Subject)
			if f.Expected != "" || f.Actual != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    expected: %s\n    actual:   %s\n", f.Expected, f.Actual)
			}
			if f.Note != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    note: %s\n", f.Note)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "lockfile: %s\n", lockPath)

		if len(d.Findings) > 0 && !driftExitZero {
			return fmt.Errorf("%d drift finding(s)", len(d.Findings))
		}
		return nil
	},
}

func init() {
	driftCmd.Flags().BoolVar(&driftNoUpstreamRotation, "no-upstream-rotation", false, "skip the upstream-tag-rotation check")
	driftCmd.Flags().BoolVar(&driftNoDownstreamTampering, "no-downstream-tampering", false, "skip the downstream-digest check")
	driftCmd.Flags().BoolVar(&driftNoNewVersions, "no-new-versions", false, "skip the new-upstream-version check")
	driftCmd.Flags().BoolVar(&driftExitZero, "exit-zero", false, "exit 0 even when drift is detected")
}
