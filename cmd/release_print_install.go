package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/release"
	"github.com/spf13/cobra"
)

var releasePrintInstallCmd = &cobra.Command{
	Use:          "print-install [dir]",
	SilenceUsage: true,
	Short:        "Print the helm upgrade --install command for the locked artifact",
	Long: `Read <dir>/chart.json + <dir>/chart-lock.json and emit a
bash-runnable 'helm upgrade --install' invocation against the locked
artifact: the wrapper when chart-lock.json#wrap exists, otherwise the
bare mirrored chart.

Output is one statement spread across multiple lines with backslash
continuations and is preceded by audit comments naming the source
(wrap | mirror), the chart ref, and the manifest digest.

mhelm does NOT execute helm. Pipe the output into bash, eval it, or
paste it into your deploy script:

  mhelm release print-install platform/cilium | bash

Pure read — no network, no writes.`,
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

		plan, err := release.Resolve(cf, lf)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), plan.Render())
		return nil
	},
}
