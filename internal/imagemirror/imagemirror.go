// Package imagemirror crane-copies container images from upstream registries
// to a single downstream OCI registry, preserving the upstream's path so the
// destination layout self-describes its origin.
package imagemirror

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gilsstudio/mhelm/internal/insecure"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
)

// Result is what Mirror returns for each image (or each image-copy attempt).
type Result struct {
	UpstreamRef      string
	DownstreamRef    string
	DownstreamDigest string
	Skipped          bool   // destination already had expected digest
	Err              error  // non-nil if this image failed to mirror
}

// MaxParallel caps the number of concurrent crane copies.
const MaxParallel = 8

// Mirror copies every (upstream-ref, expected-digest) pair to the downstream
// registry under mirrorPrefix. Operations are concurrent (cap MaxParallel)
// and idempotent (HEAD the destination first; skip when its digest already
// equals the upstream-pinned digest).
//
// mirrorPrefix is the images namespace — mirrorlayout.ImagePrefix(downstream
// .url), e.g. "ghcr.io/myorg/mirror/images". The caller resolves it (not the
// raw downstream URL) so this push and imagevalues' rewrite share one source
// of truth and cannot diverge.
func Mirror(ctx context.Context, refs []Input, mirrorPrefix string) []Result {
	results := make([]Result, len(refs))
	sem := make(chan struct{}, MaxParallel)
	var wg sync.WaitGroup
	for i, r := range refs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, r Input) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = mirrorOne(r, mirrorPrefix)
		}(i, r)
	}
	wg.Wait()
	return results
}

// Input pairs an upstream ref with the digest the lockfile pins it at.
// The pinned digest powers the "already mirrored, skip" check.
type Input struct {
	UpstreamRef    string
	UpstreamDigest string
}

func mirrorOne(in Input, mirrorPrefix string) Result {
	res := Result{UpstreamRef: in.UpstreamRef}

	dst, err := destRef(in.UpstreamRef, mirrorPrefix)
	if err != nil {
		res.Err = fmt.Errorf("build dest ref: %w", err)
		return res
	}
	res.DownstreamRef = dst

	var copyOpts []crane.Option
	var digestOpts []crane.Option
	if insecure.Enabled() {
		copyOpts = append(copyOpts, crane.Insecure)
		digestOpts = append(digestOpts, crane.Insecure)
	}

	if in.UpstreamDigest != "" {
		if existing, err := crane.Digest(dst, digestOpts...); err == nil && existing == in.UpstreamDigest {
			res.DownstreamDigest = existing
			res.Skipped = true
			return res
		}
	}

	if err := crane.Copy(in.UpstreamRef, dst, copyOpts...); err != nil {
		res.Err = fmt.Errorf("crane copy %s → %s: %w", in.UpstreamRef, dst, err)
		return res
	}
	if d, err := crane.Digest(dst, digestOpts...); err == nil {
		res.DownstreamDigest = d
	}
	return res
}

// destRef builds the mirror destination, preserving the source as written
// so chart-rendered image strings line up with mirror-values.yaml's
// rewrites. Port colons in self-hosted upstream registries are sanitised
// (`localhost:5000/foo` → `localhost_5000/foo`) so the reference parser
// doesn't confuse the port with the tag separator.
func destRef(src, mirrorPrefix string) (string, error) {
	if _, err := name.ParseReference(src); err != nil {
		return "", err
	}
	suffix := src
	if i := strings.Index(suffix, "/"); i >= 0 {
		regPart := suffix[:i]
		rest := suffix[i:]
		if strings.Contains(regPart, ":") {
			regPart = strings.Replace(regPart, ":", "_", 1)
			suffix = regPart + rest
		}
	}
	return mirrorPrefix + "/" + suffix, nil
}
