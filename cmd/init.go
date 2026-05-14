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
	initUpstreamType    string
	initUpstreamURL     string
	initUpstreamName    string
	initUpstreamVersion string
	initDownstreamURL   string
)

const (
	chartFileName            = "chart.json"
	lockFileName             = "chart-lock.json"
	imageValuesFileName      = "image-values.yaml"
	mirrorProvenanceFileName = "mirror-provenance.json"
	slsaProvenanceFileName   = "slsa-provenance.json"
)

var initCmd = &cobra.Command{
	Use:   "init [dir]",
	Short: "Scaffold a chart.json in the given directory (default: current directory)",
	Long: `Scaffold a chart.json describing upstream and downstream chart endpoints.

The file is written to <dir>/chart.json. If <dir> is omitted, the current
directory is used. If <dir> does not exist, it is created.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}

		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", dir, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created directory %s\n", dir)
		} else if err != nil {
			return err
		}

		out := filepath.Join(dir, chartFileName)
		if _, err := os.Stat(out); err == nil {
			fmt.Fprintf(os.Stderr, "%s already exists\n", out)
			return nil
		}

		f := chartfile.File{
			APIVersion: chartfile.APIVersion,
			Mirror: chartfile.Mirror{
				Upstream: chartfile.Endpoint{
					Type:    initUpstreamType,
					URL:     initUpstreamURL,
					Name:    initUpstreamName,
					Version: initUpstreamVersion,
				},
				Downstream: chartfile.Endpoint{
					Type: chartfile.TypeOCI,
					URL:  initDownstreamURL,
				},
			},
		}
		data, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&initUpstreamType, "upstream-type", chartfile.TypeRepo, "upstream type: repo or oci")
	initCmd.Flags().StringVar(&initUpstreamURL, "upstream-url", "", "upstream URL (repo base URL or oci://registry/path/chart)")
	initCmd.Flags().StringVar(&initUpstreamName, "upstream-name", "", "upstream chart name (required for type=repo)")
	initCmd.Flags().StringVar(&initUpstreamVersion, "upstream-version", "", "upstream chart version")
	initCmd.Flags().StringVar(&initDownstreamURL, "downstream-url", "", "downstream OCI URL without chart name (oci://registry/path)")
}
