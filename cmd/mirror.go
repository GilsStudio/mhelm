package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/imagemirror"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/mirror"
	"github.com/gilsstudio/mhelm/internal/mirrorlayout"
	"github.com/spf13/cobra"
)

var mirrorCmd = &cobra.Command{
	Use:   "mirror [dir]",
	Short: "Mirror the chart described in <dir>/chart.json — chart + every image — to the downstream OCI registry",
	Long: `Read <dir>/chart.json, pull the upstream chart, push it to the downstream
OCI registry, then crane-copy every image listed in <dir>/chart-lock.json#images
(populated by a prior 'mhelm discover' run) to that same registry.

Images are mirrored concurrently and idempotently: a destination whose digest
already matches the lockfile's pinned upstream digest is skipped.

The lockfile is updated in place with the downstream chart digest and per-image
downstream refs and digests.`,
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

		// Read existing lockfile so we can preserve discover's images section
		// and merge mirror's results into per-image entries.
		lf, err := lockfile.Read(lockPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read %s: %w", lockPath, err)
		}

		// 1. Push the chart.
		res, err := mirror.Run(cmd.Context(), cf)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "mirrored chart %s:%s → %s\n", res.ChartName, res.ChartVersion, res.DownstreamRef)

		// 2. Mirror every image in lockfile.mirror.images[]. The images/
		// namespace MUST match imagevalues.BuildTagBased's rewrite prefix
		// — both go through mirrorlayout.ImagePrefix so they can't drift.
		mirrorPrefix := mirrorlayout.ImagePrefix(cf.Mirror.Downstream.URL)
		inputs := make([]imagemirror.Input, len(lf.Mirror.Images))
		for i, img := range lf.Mirror.Images {
			inputs[i] = imagemirror.Input{UpstreamRef: img.Ref, UpstreamDigest: img.Digest}
		}
		imgResults := imagemirror.Mirror(cmd.Context(), inputs, mirrorPrefix)

		var failures int
		for i, r := range imgResults {
			if r.Err != nil {
				failures++
				fmt.Fprintf(cmd.OutOrStdout(), "  [FAIL] %s: %v\n", r.UpstreamRef, r.Err)
				continue
			}
			lf.Mirror.Images[i].DownstreamRef = r.DownstreamRef
			lf.Mirror.Images[i].DownstreamDigest = r.DownstreamDigest
			status := "copied"
			if r.Skipped {
				status = "skipped"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s → %s\n", status, r.UpstreamRef, r.DownstreamRef)
		}

		// 3. Write the merged lockfile.
		lf.APIVersion = lockfile.APIVersion
		lf.Mirror.Chart = lockfile.Chart{Name: res.ChartName, Version: res.ChartVersion}
		lf.Mirror.Upstream = lockfile.Upstream{
			Type:               cf.Mirror.Upstream.Type,
			URL:                cf.Mirror.Upstream.URL,
			ChartContentDigest: res.ChartContentDigest,
			OCIManifestDigest:  res.UpstreamManifestDigest,
		}
		lf.Mirror.Downstream = lockfile.Downstream{
			Ref:               res.DownstreamRef,
			OCIManifestDigest: res.DownstreamManifestDigest,
		}
		lf.Mirror.Tool = "mhelm mirror"
		lf.Mirror.Version = Version
		lf.Mirror.Timestamp = time.Now().UTC()
		if err := lockfile.Write(lockPath, lf); err != nil {
			return fmt.Errorf("write lockfile %s: %w", lockPath, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "lockfile: %s\n", lockPath)
		if failures > 0 {
			return fmt.Errorf("%d image(s) failed to mirror", failures)
		}
		return nil
	},
}
