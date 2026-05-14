package cmd

import "github.com/spf13/cobra"

// Version is the CLI version, overridable at build time via:
//
//	go build -ldflags "-X github.com/gilsstudio/mhelm/cmd.Version=v0.1.0"
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "mhelm",
	Short: "Mirror Helm charts to a private OCI registry",
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(discoverCmd)
	rootCmd.AddCommand(mirrorCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
