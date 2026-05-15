package cmd

import (
	"encoding/json"
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
	refsJSON         bool
)

// refEntry is one machine-readable row emitted by `mhelm refs --json`.
type refEntry struct {
	Kind        string `json:"kind"` // "chart" | "image"
	Ref         string `json:"ref"`  // downstream ref@digest
	UpstreamRef string `json:"upstreamRef,omitempty"`
}

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
upstream OCI artifact exists to copy from).

With --json the same selection is emitted as a JSON array of
{kind, ref, upstreamRef} objects (upstreamRef only with --with-upstream)
for downstream verification pipelines.`,
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

		var cfp *chartfile.File
		if refsWithUpstream {
			cf, err := chartfile.Load(filepath.Join(dir, chartFileName))
			if err != nil {
				return fmt.Errorf("load chart.json: %w", err)
			}
			cfp = &cf
		}

		if refsJSON {
			b, err := json.MarshalIndent(collectRefEntries(cfp, lf), "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(b))
			return nil
		}

		if refsWithUpstream {
			return printUpstreamPairs(cmd.OutOrStdout(), *cfp, lf)
		}

		if !refsImagesOnly {
			if r := digestForm(lf.Mirror.Downstream.Ref, lf.Mirror.Downstream.OCIManifestDigest); r != "" {
				fmt.Fprintln(cmd.OutOrStdout(), r)
			}
			if lf.Wrap != nil {
				if r := digestForm(lf.Wrap.Chart.Ref, lf.Wrap.Chart.OCIManifestDigest); r != "" {
					fmt.Fprintln(cmd.OutOrStdout(), r)
				}
			}
		}
		if !refsChartOnly {
			for _, img := range lf.Mirror.Images {
				if r := digestForm(img.DownstreamRef, img.DownstreamDigest); r != "" {
					fmt.Fprintln(cmd.OutOrStdout(), r)
				}
			}
		}
		return nil
	},
}

func printUpstreamPairs(w stringWriter, cf chartfile.File, lf lockfile.File) error {
	if !refsImagesOnly && cf.Mirror.Upstream.Type == chartfile.TypeOCI {
		upstreamRef := strings.TrimPrefix(cf.Mirror.Upstream.URL, "oci://") + ":" + cf.Mirror.Upstream.Version
		up := digestForm(upstreamRef, lf.Mirror.Upstream.OCIManifestDigest)
		down := digestForm(lf.Mirror.Downstream.Ref, lf.Mirror.Downstream.OCIManifestDigest)
		if up != "" && down != "" {
			fmt.Fprintf(w, "%s\t%s\n", up, down)
		}
	}
	if !refsChartOnly {
		for _, img := range lf.Mirror.Images {
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

// collectRefEntries builds the --json rows, honoring --chart-only /
// --images-only. UpstreamRef is populated only when cf != nil (i.e.
// --with-upstream), mirroring the text mode's upstream-pair gating.
func collectRefEntries(cf *chartfile.File, lf lockfile.File) []refEntry {
	out := []refEntry{}
	if !refsImagesOnly {
		if ref := digestForm(lf.Mirror.Downstream.Ref, lf.Mirror.Downstream.OCIManifestDigest); ref != "" {
			e := refEntry{Kind: "chart", Ref: ref}
			if cf != nil && cf.Mirror.Upstream.Type == chartfile.TypeOCI {
				upstreamRef := strings.TrimPrefix(cf.Mirror.Upstream.URL, "oci://") + ":" + cf.Mirror.Upstream.Version
				e.UpstreamRef = digestForm(upstreamRef, lf.Mirror.Upstream.OCIManifestDigest)
			}
			out = append(out, e)
		}
		if lf.Wrap != nil {
			if ref := digestForm(lf.Wrap.Chart.Ref, lf.Wrap.Chart.OCIManifestDigest); ref != "" {
				out = append(out, refEntry{Kind: "chart", Ref: ref})
			}
		}
	}
	if !refsChartOnly {
		for _, img := range lf.Mirror.Images {
			ref := digestForm(img.DownstreamRef, img.DownstreamDigest)
			if ref == "" {
				continue
			}
			e := refEntry{Kind: "image", Ref: ref}
			if cf != nil {
				e.UpstreamRef = digestForm(img.Ref, img.Digest)
			}
			out = append(out, e)
		}
	}
	return out
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
	refsCmd.Flags().BoolVar(&refsJSON, "json", false, "emit a JSON array instead of plain lines (combine with --with-upstream to include upstreamRef)")
}
