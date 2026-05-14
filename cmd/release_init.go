package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/spf13/cobra"
)

var (
	releaseInitName      string
	releaseInitNamespace string
	releaseInitValues    []string
)

var releaseInitCmd = &cobra.Command{
	Use:          "init [dir]",
	SilenceUsage: true,
	Short:        "Scaffold or update the release section in chart.json",
	Long: `Set chart.json's release section in <dir> (default: current directory)
from the provided flags. Other sections (mirror, wrap) are preserved
verbatim.

  mhelm release init platform/cilium \
      --name cilium \
      --namespace kube-system \
      --values-file helm/install-overrides.yml

Idempotent: re-running replaces the release block, never merges. The
overall apiVersion stays mhelm.io/v1alpha1.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		chartPath := filepath.Join(dir, chartFileName)

		cf, err := chartfile.Load(chartPath)
		if err != nil {
			return fmt.Errorf("load %s: %w", chartPath, err)
		}
		cf.Release = &chartfile.Release{
			Name:        releaseInitName,
			Namespace:   releaseInitNamespace,
			ValuesFiles: releaseInitValues,
		}
		if err := cf.Validate(); err != nil {
			return err
		}
		data, err := json.MarshalIndent(cf, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal chart.json: %w", err)
		}
		if err := os.WriteFile(chartPath, append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", chartPath, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "updated %s — release.name=%s release.namespace=%s\n",
			chartPath, releaseInitName, releaseInitNamespace)
		return nil
	},
}

func init() {
	releaseInitCmd.Flags().StringVar(&releaseInitName, "name", "", "Helm release name (required)")
	releaseInitCmd.Flags().StringVar(&releaseInitNamespace, "namespace", "", "Kubernetes namespace (required)")
	releaseInitCmd.Flags().StringArrayVar(&releaseInitValues, "values-file", nil, "values file to layer on top of the wrapper's baked-in values (repeatable)")
	_ = releaseInitCmd.MarkFlagRequired("name")
	_ = releaseInitCmd.MarkFlagRequired("namespace")
}
