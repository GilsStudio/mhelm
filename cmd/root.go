package cmd

import "github.com/spf13/cobra"

// Version is the CLI version, overridable at build time via:
//
//	go build -ldflags "-X github.com/gilsstudio/mhelm/cmd.Version=v0.1.0"
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "mhelm",
	Short: "Mirror Helm charts to a private OCI registry with supply-chain provenance",
	Long: `mhelm (Mirror HELM) is the scaffolding half of a chart-mirroring framework.

It produces two files that describe how a single Helm chart — and every
container image it references — should be mirrored from an upstream source
to a private OCI registry:

  chart.json       user-edited input spec (upstream ref + downstream registry)
  chart-lock.json  generated source of truth: pinned digests, mirror metadata

The mhelm GitHub Action consumes these files in CI to perform the actual
mirror, cosign-sign every artifact via ambient OIDC, and attach SBOM / vuln /
SLSA / MirrorProvenance attestations. The CLI itself is network-read-only:
signing keys and registry credentials never need to leave CI.

See README.md for the full documentation.`,
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(discoverCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(mirrorCmd)
	rootCmd.AddCommand(provenanceCmd)
	rootCmd.AddCommand(slsaCmd)
	rootCmd.AddCommand(refsCmd)
	rootCmd.AddCommand(driftCmd)
	rootCmd.AddCommand(vulnGateCmd)
	rootCmd.AddCommand(versionCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
