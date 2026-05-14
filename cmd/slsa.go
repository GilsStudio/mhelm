package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/slsa"
	"github.com/spf13/cobra"
)

var slsaCmd = &cobra.Command{
	Use:   "slsa [dir]",
	Short: "Write the SLSA v1 build provenance predicate as JSON",
	Long: `Read <dir>/chart.json + <dir>/chart-lock.json, build a SLSA v1
build provenance predicate (https://slsa.dev/spec/v1.0/provenance) describing
the mhelm mirror operation, and write it to <dir>/slsa-provenance.json.

resolvedDependencies lists every upstream chart and image with their content
digests so verifiers can confirm bytes. builder.id and source come from the
GitHub Actions environment.

The mhelm GitHub Action passes this file to cosign:

    cosign attest --predicate slsa-provenance.json \
        --type slsaprovenance1 \
        <downstream-ref>@<digest>

Pure read+write — no network.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		chartPath := filepath.Join(dir, chartFileName)
		lockPath := filepath.Join(dir, lockFileName)
		outPath := filepath.Join(dir, slsaProvenanceFileName)

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

		p := slsa.Build(cf, lf, dir, Version)
		if err := slsa.Write(outPath, p); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", outPath)
		fmt.Fprintf(cmd.OutOrStdout(), "  buildType: %s\n", p.BuildDefinition.BuildType)
		fmt.Fprintf(cmd.OutOrStdout(), "  resolvedDependencies: %d\n", len(p.BuildDefinition.ResolvedDependencies))
		if p.RunDetails.Builder.ID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  builder: %s\n", p.RunDetails.Builder.ID)
		}
		return nil
	},
}
