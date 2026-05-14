package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/cobra"
)

var (
	refsChartOnly    bool
	refsImagesOnly   bool
	refsWithUpstream bool
)

var refsCmd = &cobra.Command{
	Use:   "refs [dir]",
	Short: "Print downstream ref@digest for every mirrored artifact, one per line",
	Long: `Read <dir>/chart-lock.json and emit ref@digest for the chart and each
image that has been mirrored (i.e. has both downstreamRef and downstreamDigest
set by 'mhelm mirror').

Designed for shell piping in the mhelm GitHub Action:

    mhelm refs platform/cert-manager | while read ref; do
        cosign sign --yes "$ref"
        cosign attest --predicate sbom.json --type cyclonedx "$ref"
    done

With --with-upstream, output is TAB-separated upstream@digest \t downstream@digest
pairs suitable for 'cosign copy' to forward upstream attestations to the
downstream registry. The chart row is omitted for type=repo upstreams (no
upstream OCI artifact exists to copy from).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		lockPath := filepath.Join(dir, lockFileName)

		lf, err := lockfile.Read(lockPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%s not found — run `mhelm mirror` first", lockPath)
			}
			return fmt.Errorf("read %s: %w", lockPath, err)
		}

		if refsWithUpstream {
			cf, err := chartfile.Load(filepath.Join(dir, chartFileName))
			if err != nil {
				return fmt.Errorf("load chart.json: %w", err)
			}
			return printUpstreamPairs(cmd.OutOrStdout(), cf, lf)
		}

		if !refsImagesOnly {
			if r := digestForm(lf.Downstream.Ref, lf.Downstream.OCIManifestDigest); r != "" {
				fmt.Fprintln(cmd.OutOrStdout(), r)
			}
		}
		if !refsChartOnly {
			for _, img := range lf.Images {
				if r := digestForm(img.DownstreamRef, img.DownstreamDigest); r != "" {
					fmt.Fprintln(cmd.OutOrStdout(), r)
				}
			}
		}
		return nil
	},
}

func printUpstreamPairs(w stringWriter, cf chartfile.File, lf lockfile.File) error {
	if !refsImagesOnly && cf.Upstream.Type == chartfile.TypeOCI {
		upstreamRef := strings.TrimPrefix(cf.Upstream.URL, "oci://") + ":" + cf.Upstream.Version
		up := digestForm(upstreamRef, lf.Upstream.OCIManifestDigest)
		down := digestForm(lf.Downstream.Ref, lf.Downstream.OCIManifestDigest)
		if up != "" && down != "" {
			fmt.Fprintf(w, "%s\t%s\n", up, down)
		}
	}
	if !refsChartOnly {
		for _, img := range lf.Images {
			up := digestForm(img.Ref, img.Digest)
			down := digestForm(img.DownstreamRef, img.DownstreamDigest)
			if up == "" || down == "" {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\n", up, down)
		}
	}
	return nil
}

// digestForm strips the tag from ref and appends @digest. Returns empty
// string when either component is missing (entry skipped).
func digestForm(ref, digest string) string {
	if ref == "" || digest == "" {
		return ""
	}
	p, err := name.ParseReference(ref)
	if err != nil {
		return ""
	}
	return p.Context().Name() + "@" + digest
}

// stringWriter is the subset of io.Writer that fmt.Fprintln/Fprintf need.
type stringWriter interface {
	Write(p []byte) (int, error)
}

func init() {
	refsCmd.Flags().BoolVar(&refsChartOnly, "chart-only", false, "print only the chart's ref@digest")
	refsCmd.Flags().BoolVar(&refsImagesOnly, "images-only", false, "print only image ref@digests")
	refsCmd.Flags().BoolVar(&refsWithUpstream, "with-upstream", false, "emit TAB-separated upstream@digest <TAB> downstream@digest pairs")
}
