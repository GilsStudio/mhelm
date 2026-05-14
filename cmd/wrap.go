package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/wrap"
	"github.com/spf13/cobra"
)

var wrapCmd = &cobra.Command{
	Use:          "wrap [dir]",
	SilenceUsage: true,
	Short:        "Author and push a wrapper Helm chart that pins all images to the mirror",
	Long: `Read <dir>/chart.json + <dir>/chart-lock.json and, when chart.json has
a wrap section, author a wrapper Helm chart that depends on the mirrored
upstream, bakes in digest-pinned image rewrites derived from the lockfile,
bundles any wrap.extraManifests, packages it, and pushes the result to the
downstream OCI registry.

The wrapper is the user-facing artifact for adopters who want a single
signed, locked, attested chart representing "the cluster's view" of the
upstream — opposite path from the lightweight no-wrap adoption that uses
image-values.yaml at install time.

A wrap fail-safe rejects wrappers whose rendered images aren't already
mirrored — those must first be added to mirror.discoveryValues or
mirror.extraImages and re-mirrored. Without that gate, a wrapper could
silently bypass the mirror by pulling from upstream registries at
install time.

When chart.json has no wrap section the command is a no-op and exits 0,
so it is safe to invoke unconditionally from CI.`,
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
		if cf.Wrap == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "no wrap section in chart.json — skipping")
			return nil
		}
		if err := cf.Validate(); err != nil {
			return err
		}

		lf, err := lockfile.Read(lockPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%s not found — run `mhelm mirror` first", lockPath)
			}
			return fmt.Errorf("read %s: %w", lockPath, err)
		}

		res, err := wrap.Run(cmd.Context(), cf, lf, dir)
		if err != nil {
			return err
		}

		block := res.ToLockfileBlock(Version, time.Now().UTC())
		lf.Wrap = &block
		if err := lockfile.Write(lockPath, lf); err != nil {
			return fmt.Errorf("write %s: %w", lockPath, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "wrapped %s:%s → %s\n", res.ChartName, res.ChartVersion, res.DownstreamRef)
		fmt.Fprintf(cmd.OutOrStdout(), "  content digest: %s\n", res.ChartContentDigest)
		if res.DownstreamManifestDigest != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  manifest digest: %s\n", res.DownstreamManifestDigest)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  depends on:     %s\n", res.DependsOnRef)
		fmt.Fprintf(cmd.OutOrStdout(), "  deployedImages: %d\n", len(res.DeployedImages))
		fmt.Fprintf(cmd.OutOrStdout(), "lockfile: %s\n", lockPath)
		return nil
	},
}
