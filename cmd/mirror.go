package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/mirror"
	"github.com/spf13/cobra"
)

var mirrorCmd = &cobra.Command{
	Use:   "mirror [dir]",
	Short: "Mirror the chart described in <dir>/chart.json to the downstream OCI registry",
	Long: `Read <dir>/chart.json, pull the upstream chart, push it to the downstream
OCI registry, and write <dir>/chart-lock.json with the pinned digests.

If <dir>/chart-lock.json already exists (e.g. produced by a prior
'mhelm discover' run), the images section is preserved.`,
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
		if err := cf.Validate(); err != nil {
			return err
		}
		res, err := mirror.Run(cmd.Context(), cf)
		if err != nil {
			return err
		}

		// Preserve fields owned by other commands (notably Images set by discover).
		lf, err := lockfile.Read(lockPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read %s: %w", lockPath, err)
		}
		lf.SchemaVersion = lockfile.SchemaVersion
		lf.Chart = lockfile.Chart{Name: res.ChartName, Version: res.ChartVersion}
		lf.Upstream = lockfile.Upstream{
			Type:               cf.Upstream.Type,
			URL:                cf.Upstream.URL,
			ChartContentDigest: res.ChartContentDigest,
			OCIManifestDigest:  res.UpstreamManifestDigest,
		}
		lf.Downstream = lockfile.Downstream{
			Ref:               res.DownstreamRef,
			OCIManifestDigest: res.DownstreamManifestDigest,
		}
		lf.Mirror = lockfile.Mirror{
			Tool:      "mhelm mirror",
			Version:   Version,
			Timestamp: time.Now().UTC(),
		}
		if err := lockfile.Write(lockPath, lf); err != nil {
			return fmt.Errorf("write lockfile %s: %w", lockPath, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "mirrored chart %s:%s → %s\n", res.ChartName, res.ChartVersion, res.DownstreamRef)
		fmt.Fprintf(cmd.OutOrStdout(), "  chart digest: %s\n", res.ChartContentDigest)
		fmt.Fprintf(cmd.OutOrStdout(), "  downstream:   %s\n", res.DownstreamManifestDigest)
		fmt.Fprintf(cmd.OutOrStdout(), "  lockfile:     %s\n", lockPath)
		return nil
	},
}
