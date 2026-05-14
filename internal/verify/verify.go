// Package verify performs upstream cosign signature verification against
// the sigstore public-good roots (Fulcio + Rekor + CTLog). For each ref
// in the lockfile, it records a lockfile.Signature describing whether a
// valid signature exists and, if so, who signed it.
//
// Used by `mhelm verify` between `mhelm discover` and `mhelm mirror` so
// the lockfile carries provenance metadata before any artifact is copied.
package verify

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/insecure"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/sigstore/cosign/v2/cmd/cosign/cli/fulcio"
	"github.com/sigstore/cosign/v2/pkg/cosign"
	ociremote "github.com/sigstore/cosign/v2/pkg/oci/remote"
)

// Result holds per-ref signature results so the caller can merge them
// back into lockfile.File.
type Result struct {
	// Images keyed by upstream ref.
	Images map[string]*lockfile.Signature
}

// Run verifies every image ref in lf.Images against sigstore public-good
// trust roots. cf.TrustedIdentities — when non-empty — restricts accepted
// signatures to identities matching the allowlist; otherwise any valid
// signature is recorded.
func Run(ctx context.Context, cf chartfile.File, lf lockfile.File) (Result, error) {
	res := Result{Images: map[string]*lockfile.Signature{}}

	co, err := buildCheckOpts(ctx, cf.TrustedIdentities)
	if err != nil {
		return res, fmt.Errorf("build cosign check opts: %w", err)
	}

	for _, img := range lf.Images {
		sig := verifyOne(ctx, img.Ref, co)
		res.Images[img.Ref] = sig
	}
	return res, nil
}

func buildCheckOpts(ctx context.Context, trusted []chartfile.TrustedIdentity) (*cosign.CheckOpts, error) {
	rootCerts, err := fulcio.GetRoots()
	if err != nil {
		return nil, fmt.Errorf("fulcio roots: %w", err)
	}
	intermediates, err := fulcio.GetIntermediates()
	if err != nil {
		return nil, fmt.Errorf("fulcio intermediates: %w", err)
	}
	rekorPubs, err := cosign.GetRekorPubs(ctx)
	if err != nil {
		return nil, fmt.Errorf("rekor pubs: %w", err)
	}
	ctlogPubs, err := cosign.GetCTLogPubs(ctx)
	if err != nil {
		return nil, fmt.Errorf("ctlog pubs: %w", err)
	}

	co := &cosign.CheckOpts{
		RootCerts:         rootCerts,
		IntermediateCerts: intermediates,
		RekorPubKeys:      rekorPubs,
		CTLogPubKeys:      ctlogPubs,
		ClaimVerifier:     cosign.SimpleClaimVerifier,
	}

	for _, t := range trusted {
		co.Identities = append(co.Identities, cosign.Identity{
			Issuer:        t.Issuer,
			SubjectRegExp: t.SubjectRegex,
		})
	}

	return co, nil
}

func verifyOne(ctx context.Context, ref string, co *cosign.CheckOpts) *lockfile.Signature {
	parseOpts := []name.Option{}
	if insecure.Enabled() {
		parseOpts = append(parseOpts, name.Insecure)
	}
	parsed, err := name.ParseReference(ref, parseOpts...)
	if err != nil {
		return &lockfile.Signature{Type: "error", Error: err.Error()}
	}

	// ociremote.SignatureTag and friends use these registry opts to fetch
	// the signature manifest from the same registry as the image.
	registryOpts := []ociremote.Option{}
	co.RegistryClientOpts = registryOpts

	sigs, _, err := cosign.VerifyImageSignatures(ctx, parsed, co)
	if err != nil {
		if isNotSigned(err) {
			return &lockfile.Signature{Type: "none"}
		}
		return &lockfile.Signature{Type: "error", Error: err.Error()}
	}
	if len(sigs) == 0 {
		return &lockfile.Signature{Type: "none"}
	}

	out := &lockfile.Signature{Verified: true, Type: "cosign-keyless"}
	// Pull identity from the first verified signature's cert.
	cert, certErr := sigs[0].Cert()
	if certErr == nil && cert != nil {
		out.Subject = pickSubject(cert)
		for _, ext := range cert.Extensions {
			// OIDC issuer is OID 1.3.6.1.4.1.57264.1.1 (cosign-bundle) or
			// 1.3.6.1.4.1.57264.1.8 (new format).
			oid := ext.Id.String()
			if oid == "1.3.6.1.4.1.57264.1.1" || oid == "1.3.6.1.4.1.57264.1.8" {
				out.Issuer = strings.TrimSpace(string(ext.Value))
				break
			}
		}
	}
	// Rekor log index — pulled from bundle when present.
	if b, err := sigs[0].Bundle(); err == nil && b != nil && b.Payload.LogIndex != 0 {
		out.RekorLogIndex = b.Payload.LogIndex
	}
	return out
}

// pickSubject returns the most useful identity field from a Fulcio cert.
// Keyless signers populate either SAN URIs (workflow URLs from GHA) or
// SAN emails depending on the OIDC issuer.
func pickSubject(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	if len(cert.URIs) > 0 {
		return cert.URIs[0].String()
	}
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0]
	}
	return ""
}

// isNotSigned recognises cosign's "no signatures found" sentinel so we
// can record `type=none` instead of `type=error`.
func isNotSigned(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no signatures found"):
		return true
	case strings.Contains(msg, "MANIFEST_UNKNOWN"):
		return true
	case strings.Contains(msg, "no matching signatures"):
		return true
	}
	var noMatch *cosign.ErrNoMatchingSignatures
	return errors.As(err, &noMatch)
}