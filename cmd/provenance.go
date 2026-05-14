package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/provenance"
	"github.com/spf13/cobra"
)

var provenanceCmd = &cobra.Command{
	Use:   "provenance [dir]",
	Short: "Write the custom MirrorProvenance in-toto predicate as JSON",
	Long: `Read <dir>/chart.json + <dir>/chart-lock.json, build the
'https://mhelm.dev/MirrorProvenance/v1' predicate (upstream ref + content
digest, upstream-signature verification result, downstream mirrored refs +
digests, GHA build context), and write it to <dir>/mirror-provenance.json.

The mhelm GitHub Action passes this file to cosign:

    cosign attest --predicate mirror-provenance.json \
        --type https://mhelm.dev/MirrorProvenance/v1 \
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
		outPath := filepath.Join(dir, mirrorProvenanceFileName)

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

		p := provenance.Build(cf, lf, Version)
		if err := provenance.Write(outPath, p); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", outPath)
		fmt.Fprintf(cmd.OutOrStdout(), "  subjects: chart + %d image(s)\n", len(p.Downstream.Images))
		fmt.Fprintf(cmd.OutOrStdout(), "  upstream signatures recorded: %d\n", len(p.UpstreamSignatures))
		if p.BuildContext != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  build context: GHA %s/%s run %s\n", p.BuildContext.Repository, p.BuildContext.Workflow, p.BuildContext.RunID)
		}
		return nil
	},
}
