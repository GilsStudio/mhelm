package cmd

import "github.com/spf13/cobra"

// releaseCmd is the parent for deploy-time ergonomics helpers. Invoking
// `mhelm release` without a subcommand prints the help (cobra default).
var releaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Deploy-time ergonomics: scaffold release config, print helm install commands",
	Long: `Helpers that turn chart.json + chart-lock.json into a deployable
helm command line. mhelm does NOT execute helm — the boundary stops at
print-install. Adopters pipe the output into bash or paste into their
deploy pipeline.`,
}

func init() {
	releaseCmd.AddCommand(releaseInitCmd)
	releaseCmd.AddCommand(releasePrintInstallCmd)
}
