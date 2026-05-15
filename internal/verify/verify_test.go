package verify

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/gilsstudio/mhelm/internal/chartfile"
	"github.com/gilsstudio/mhelm/internal/lockfile"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/sigstore/cosign/v2/pkg/cosign"
)

func TestAllowUnsignedIndex_CanonicalizesEntries(t *testing.T) {
	idx := allowUnsignedIndex([]string{
		"cilium/hubble-ui",
		"quay.io/cilium/hubble-relay",
	})
	// "cilium/hubble-ui" should canonicalize the same way as a real
	// pull ref of the same image, so e.g. "cilium/hubble-ui:v0.13.2"
	// hits the lookup.
	if !idx[canonicalRepo("cilium/hubble-ui:v0.13.2")] {
		t.Errorf("idx missing canonical match for cilium/hubble-ui:v0.13.2; entries: %v", idx)
	}
	if !idx[canonicalRepo("quay.io/cilium/hubble-relay:v0.13.2")] {
		t.Errorf("idx missing canonical match for quay.io/cilium/hubble-relay")
	}
	if idx[canonicalRepo("cilium/other:1")] {
		t.Errorf("idx false positive for unrelated repo")
	}
}

func TestClassifyVerifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want verdict
	}{
		// (*cosign.ErrNoMatchingSignatures has an unexported field and no
		// constructor — the typed errors.As path is exercised in prod;
		// here the string heuristics cover the not-signed verdict.)
		{"no-signatures-string", errors.New("no signatures found for image"), verdictNotSigned},
		{"manifest-unknown-string", errors.New("GET https://r/v2/.../sig: MANIFEST_UNKNOWN: manifest unknown"), verdictNotSigned},
		{"transport-404", &transport.Error{StatusCode: 404}, verdictNotSigned},
		{"transport-404-diag", &transport.Error{StatusCode: 418, Errors: []transport.Diagnostic{{Code: transport.ManifestUnknownErrorCode}}}, verdictNotSigned},
		{"transport-401", fmt.Errorf("fetch sig: %w", &transport.Error{StatusCode: 401}), verdictUnreachable},
		{"transport-403", &transport.Error{StatusCode: 403}, verdictUnreachable},
		{"transport-429", &transport.Error{StatusCode: 429}, verdictUnreachable},
		{"transport-503", &transport.Error{StatusCode: 503}, verdictUnreachable},
		{"transport-500", &transport.Error{StatusCode: 500}, verdictUnreachable},
		{"transport-400", &transport.Error{StatusCode: 400}, verdictError},
		{"context-deadline", context.DeadlineExceeded, verdictUnreachable},
		{"context-canceled", context.Canceled, verdictUnreachable},
		{"dns", &url.Error{Op: "Get", URL: "https://fulcio.sigstore.dev", Err: &net.DNSError{Name: "fulcio.sigstore.dev", IsNotFound: true}}, verdictUnreachable},
		{"conn-refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, verdictUnreachable},
		{"x509-unknown-authority", x509.UnknownAuthorityError{}, verdictUnreachable},
		{"tuf-string", errors.New("updating local metadata: error fetching TUF: rekor"), verdictUnreachable},
		{"fulcio-string", errors.New("fulcio roots: connection refused"), verdictUnreachable},
		{"garbage", errors.New("totally unexpected internal state"), verdictError},
		{"nil", nil, verdictError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyVerifyError(tc.err); got != tc.want {
				t.Errorf("classifyVerifyError(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestRun_TrustRootUnreachable_Degrades(t *testing.T) {
	orig := buildCheckOptsFn
	defer func() { buildCheckOptsFn = orig }()
	buildCheckOptsFn = func(context.Context, []chartfile.TrustedIdentity) (*cosign.CheckOpts, error) {
		return nil, &TrustRootUnreachableError{Err: errors.New("fulcio roots: i/o timeout")}
	}

	cf := chartfile.File{Mirror: chartfile.Mirror{
		Verify: chartfile.Verify{AllowUnsigned: []string{"quay.io/cilium/hubble-ui"}},
	}}
	lf := lockfile.File{Mirror: lockfile.MirrorBlock{Images: []lockfile.Image{
		{Ref: "quay.io/cilium/cilium:v1.19.3"},
		{Ref: "quay.io/cilium/hubble-ui:v0.13.2"},
	}}}

	res, err := Run(context.Background(), cf, lf)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil (degrade)", err)
	}
	if got := res.Images["quay.io/cilium/cilium:v1.19.3"]; got == nil || got.Type != "unreachable" {
		t.Errorf("cilium = %+v, want Type=unreachable", got)
	}
	if got := res.Images["quay.io/cilium/hubble-ui:v0.13.2"]; got == nil || got.Type != "allowlisted" {
		t.Errorf("hubble-ui = %+v, want Type=allowlisted (allowlist independent of reachability)", got)
	}
}

func TestRun_NonTransportTrustRootError_IsFatal(t *testing.T) {
	orig := buildCheckOptsFn
	defer func() { buildCheckOptsFn = orig }()
	buildCheckOptsFn = func(context.Context, []chartfile.TrustedIdentity) (*cosign.CheckOpts, error) {
		return nil, errors.New("programming error: nil identities")
	}
	lf := lockfile.File{Mirror: lockfile.MirrorBlock{Images: []lockfile.Image{{Ref: "x/y:1"}}}}
	if _, err := Run(context.Background(), chartfile.File{}, lf); err == nil {
		t.Fatal("Run() = nil, want fatal error for non-transport buildCheckOpts failure")
	}
}
