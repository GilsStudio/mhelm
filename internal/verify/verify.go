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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/insecure"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
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

// Run verifies every image ref in lf.Mirror.Images against sigstore
// public-good trust roots. cf.Mirror.Verify.TrustedIdentities — when
// non-empty — restricts accepted signatures to identities matching the
// allowlist; otherwise any valid signature is recorded.
// cf.Mirror.Verify.AllowUnsigned exempts listed repository paths from
// verification entirely; those entries are recorded with Type="allowlisted".
func Run(ctx context.Context, cf chartfile.File, lf lockfile.File) (Result, error) {
	res := Result{Images: map[string]*lockfile.Signature{}}

	allowed := allowUnsignedIndex(cf.Mirror.Verify.AllowUnsigned)

	co, err := buildCheckOptsFn(ctx, cf.Mirror.Verify.TrustedIdentities)
	if err != nil {
		var tru *TrustRootUnreachableError
		if errors.As(err, &tru) {
			// Air-gapped / sandboxed: the sigstore trust roots
			// (Fulcio/Rekor/CTLog/TUF) couldn't be fetched. Don't abort
			// with one fatal error — mark every non-allowlisted image
			// "unreachable" so CI gets actionable per-image output and
			// the lockfile records why.
			for _, img := range lf.Mirror.Images {
				if allowed[canonicalRepo(img.Ref)] {
					res.Images[img.Ref] = &lockfile.Signature{
						Verified:    true,
						Type:        "allowlisted",
						Allowlisted: true,
					}
					continue
				}
				res.Images[img.Ref] = &lockfile.Signature{Type: "unreachable", Error: tru.Error()}
			}
			return res, nil
		}
		return res, fmt.Errorf("build cosign check opts: %w", err)
	}

	for _, img := range lf.Mirror.Images {
		if allowed[canonicalRepo(img.Ref)] {
			res.Images[img.Ref] = &lockfile.Signature{
				Verified:    true,
				Type:        "allowlisted",
				Allowlisted: true,
			}
			continue
		}
		sig := verifyOne(ctx, img.Ref, co)
		res.Images[img.Ref] = sig
	}
	return res, nil
}

// allowUnsignedIndex normalizes the configured list into a lookup set
// keyed by canonical repo path so an entry like "cilium/hubble-ui"
// matches "quay.io/cilium/hubble-ui:v0.13.2" etc.
func allowUnsignedIndex(entries []string) map[string]bool {
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[canonicalRepo(e)] = true
	}
	return out
}

// canonicalRepo returns the registry-qualified repository path for a ref,
// stripping any tag/digest and normalizing Docker Hub's implicit prefixes
// ("nginx" → "index.docker.io/library/nginx"). Shared with the discover
// package's matcher; kept here to avoid an internal-package import cycle.
func canonicalRepo(s string) string {
	if r, err := name.ParseReference(s); err == nil {
		return r.Context().Name()
	}
	if r, err := name.NewRepository(s); err == nil {
		return r.Name()
	}
	return strings.ToLower(s)
}

// TrustRootUnreachableError marks a buildCheckOpts failure caused by not
// being able to reach the sigstore trust roots (Fulcio/Rekor/CTLog/TUF) —
// as opposed to a programming/config error. Run degrades to per-image
// "unreachable" results when it sees this, instead of aborting.
type TrustRootUnreachableError struct{ Err error }

func (e *TrustRootUnreachableError) Error() string {
	return "trust roots unreachable: " + e.Err.Error()
}
func (e *TrustRootUnreachableError) Unwrap() error { return e.Err }

// trustRootErr wraps a trust-root fetch failure. Only transport-class
// failures become a *TrustRootUnreachableError (and so degrade gracefully);
// anything else stays a plain fatal error.
func trustRootErr(stage string, err error) error {
	wrapped := fmt.Errorf("%s: %w", stage, err)
	if classifyVerifyError(err) == verdictUnreachable {
		return &TrustRootUnreachableError{Err: wrapped}
	}
	return wrapped
}

// buildCheckOptsFn is the trust-root assembly seam — overridden in tests
// to exercise the air-gapped degrade path without network.
var buildCheckOptsFn = buildCheckOpts

func buildCheckOpts(ctx context.Context, trusted []chartfile.TrustedIdentity) (*cosign.CheckOpts, error) {
	rootCerts, err := fulcio.GetRoots()
	if err != nil {
		return nil, trustRootErr("fulcio roots", err)
	}
	intermediates, err := fulcio.GetIntermediates()
	if err != nil {
		return nil, trustRootErr("fulcio intermediates", err)
	}
	rekorPubs, err := cosign.GetRekorPubs(ctx)
	if err != nil {
		return nil, trustRootErr("rekor pubs", err)
	}
	ctlogPubs, err := cosign.GetCTLogPubs(ctx)
	if err != nil {
		return nil, trustRootErr("ctlog pubs", err)
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
		switch classifyVerifyError(err) {
		case verdictNotSigned:
			return &lockfile.Signature{Type: "none"}
		case verdictUnreachable:
			return &lockfile.Signature{Type: "unreachable", Error: err.Error()}
		default:
			return &lockfile.Signature{Type: "error", Error: err.Error()}
		}
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

// verdict is the three-way classification of a cosign verify failure.
type verdict int

const (
	// verdictNotSigned: verification completed and there is genuinely no
	// (matching) signature → Signature.Type "none".
	verdictNotSigned verdict = iota
	// verdictUnreachable: verification could not complete because a trust
	// root or the image registry was unreachable → Type "unreachable".
	verdictUnreachable
	// verdictError: verification ran but failed for a non-transport,
	// non-absence reason → Type "error".
	verdictError
)

// classifyVerifyError buckets a cosign verify error into not-signed vs
// unreachable vs error. Typed (errors.As) checks run before string
// heuristics because cosign frequently flattens wrapped errors to plain
// strings, so the string rules are a backstop. Ambiguous errors map to
// verdictError — the loudest non-passing bucket — so a real failure is
// never silently downgraded to "unsigned" or masked as "infra flake".
func classifyVerifyError(err error) verdict {
	if err == nil {
		return verdictError // callers only invoke on non-nil; defensive.
	}

	// 1. Genuinely no signature (typed sentinel).
	var noMatch *cosign.ErrNoMatchingSignatures
	if errors.As(err, &noMatch) {
		return verdictNotSigned
	}

	// 2. Context cancellation / deadline.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return verdictUnreachable
	}

	// 3. Registry transport HTTP errors (the high-value case): a 404 /
	// MANIFEST_UNKNOWN on the `.sig` tag means "no signature"; auth walls,
	// rate limits and 5xx mean "couldn't determine".
	var te *transport.Error
	if errors.As(err, &te) {
		switch te.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return verdictUnreachable
		case http.StatusNotFound:
			return verdictNotSigned
		}
		for _, d := range te.Errors {
			if d.Code == transport.ManifestUnknownErrorCode {
				return verdictNotSigned
			}
		}
		return verdictError
	}

	// 4. Network / DNS / TLS / URL typed errors.
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var urlErr *url.Error
	var netErr net.Error
	var x509UA x509.UnknownAuthorityError
	var x509Inv x509.CertificateInvalidError
	var x509Host x509.HostnameError
	var tlsErr *tls.CertificateVerificationError
	if errors.As(err, &dnsErr) ||
		errors.As(err, &opErr) ||
		errors.As(err, &urlErr) ||
		errors.As(err, &netErr) ||
		errors.As(err, &x509UA) ||
		errors.As(err, &x509Inv) ||
		errors.As(err, &x509Host) ||
		errors.As(err, &tlsErr) {
		return verdictUnreachable
	}

	msg := strings.ToLower(err.Error())

	// 5. Not-signed string heuristics.
	switch {
	case strings.Contains(msg, "no signatures found"),
		strings.Contains(msg, "manifest_unknown"),
		strings.Contains(msg, "no matching signatures"):
		return verdictNotSigned
	}

	// 6. Connectivity / TUF / sigstore string fallbacks.
	for _, frag := range []string{
		"tuf", "error fetching", "fulcio", "rekor", "ctfe", "ctlog",
		"sigstore", "tls:", "x509:", "certificate signed by unknown authority",
		"connection refused", "connection reset", "no such host",
		"i/o timeout", "context deadline exceeded", "deadline",
		"server misbehaving", "network is unreachable", "timeout",
		"temporary failure in name resolution", "eof",
	} {
		if strings.Contains(msg, frag) {
			return verdictUnreachable
		}
	}

	// 7. Ambiguous → loudest non-passing bucket.
	return verdictError
}