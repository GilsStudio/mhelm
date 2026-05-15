package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/gilsstudio/mhelm/internal/verify"
	"github.com/spf13/cobra"
)

var verifyStrict bool

var verifyCmd = &cobra.Command{
	Use:   "verify [dir]",
	Short: "Verify upstream cosign signatures for every image in chart-lock.json",
	Long: `Read <dir>/chart-lock.json (populated by 'mhelm discover'), check each
image ref for a cosign signature against sigstore public-good trust roots
(Fulcio + Rekor + CTLog), and write the result back as the image's
'signature' field.

When chart.json#trustedIdentities is set, only signatures whose subject +
issuer match the allowlist are accepted; others are recorded as unverified.

An image whose signature could not be checked because the sigstore trust
roots or the registry were unreachable (air-gapped CI, blocked egress) is
recorded as type "unreachable" — distinct from "none" (verification ran;
genuinely no signature). Both count as unverified.

Default exit code is 0 regardless of signature outcomes — the lockfile diff
is the audit. Use --strict to fail on any unverified image (--strict also
fails on "unreachable"; an air-gapped runner must provide offline trust
roots or accept the failure).

Network reads only — no pushes.`,
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
				return fmt.Errorf("%s not found — run `mhelm discover` first", lockPath)
			}
			return fmt.Errorf("read %s: %w", lockPath, err)
		}
		if len(lf.Mirror.Images) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no images in lockfile — nothing to verify")
			return nil
		}

		res, err := verify.Run(cmd.Context(), cf, lf)
		if err != nil {
			return err
		}

		var unverified, signed, noneCount, unreachableCount, errCount int
		for i, img := range lf.Mirror.Images {
			sig := res.Images[img.Ref]
			lf.Mirror.Images[i].Signature = sig
			label := "✗"
			if sig.Verified {
				label = "✓"
			}
			ident := sig.Subject
			if sig.Issuer != "" {
				ident = fmt.Sprintf("%s (%s)", sig.Subject, sig.Issuer)
			}
			switch sig.Type {
			case "cosign-keyless":
				signed++
				fmt.Fprintf(cmd.OutOrStdout(), "  %s %s  signed: %s\n", label, img.Ref, ident)
			case "allowlisted":
				fmt.Fprintf(cmd.OutOrStdout(), "  %s %s  unsigned (allowlisted via mirror.verify.allowUnsigned)\n", label, img.Ref)
			case "none":
				noneCount++
				unverified++
				fmt.Fprintf(cmd.OutOrStdout(), "  %s %s  unsigned\n", label, img.Ref)
			case "unreachable":
				unreachableCount++
				unverified++
				fmt.Fprintf(cmd.OutOrStdout(), "  %s %s  could not verify (trust root / registry unreachable): %s\n", label, img.Ref, sig.Error)
			case "error":
				errCount++
				unverified++
				fmt.Fprintf(cmd.OutOrStdout(), "  %s %s  verify error: %s\n", label, img.Ref, sig.Error)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "summary: %d signed, %d unsigned, %d unreachable, %d error\n",
			signed, noneCount, unreachableCount, errCount)

		if err := lockfile.Write(lockPath, lf); err != nil {
			return fmt.Errorf("write %s: %w", lockPath, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "lockfile: %s\n", lockPath)

		if verifyStrict && unverified > 0 {
			return fmt.Errorf("%d image(s) unverified (--strict): %d unsigned, %d unreachable, %d error",
				unverified, noneCount, unreachableCount, errCount)
		}
		return nil
	},
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyStrict, "strict", false, "exit non-zero on any unverified image")
}
