package discover

import "github.com/gilsstudio/mhelm/internal/lockfile"

// Extractors emit (ref, source) candidates from rendered chart manifests.
// Each extractor handles one pattern. Candidates are validated and deduped
// by the orchestrator in discover.go.

// candidate is a discovered image reference together with how it was found.
// `Trusted` candidates are kept even when crane.Digest can't resolve them
// (manifest-walked + manual entries are confidently real). `!Trusted`
// candidates are dropped on resolution failure (regex sources need
// registry confirmation to filter false positives).
type candidate struct {
	Ref           string
	Source        string
	DiscoveredVia string
	Trusted       bool
}

// extractFromContainers collects `containers[].image` and
// `initContainers[].image`. This is the precise pre-existing extractor;
// any image here is genuinely a container image.
func extractFromContainers(doc map[string]any) []candidate {
	var out []candidate
	walkContainerImages(doc, false, &out)
	return out
}

func walkContainerImages(node any, underContainers bool, out *[]candidate) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			switch k {
			case "image":
				if underContainers {
					if s, ok := child.(string); ok && s != "" {
						*out = append(*out, candidate{Ref: s, Source: lockfile.SourceManifest, Trusted: true})
					}
				}
			case "containers", "initContainers":
				walkContainerImages(child, true, out)
			default:
				walkContainerImages(child, false, out)
			}
		}
	case []any:
		for _, item := range v {
			walkContainerImages(item, underContainers, out)
		}
	}
}

// extractFromEnv collects `containers[].env[].value` strings that look
// like image refs. Operators commonly pass child-image refs as env-var
// defaults to themselves.
func extractFromEnv(doc map[string]any) []candidate {
	var out []candidate
	walkEnv(doc, false, &out)
	return out
}

func walkEnv(node any, underContainers bool, out *[]candidate) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			switch k {
			case "containers", "initContainers":
				walkEnv(child, true, out)
			case "env":
				if underContainers {
					collectEnvValues(child, out)
				}
				walkEnv(child, underContainers, out)
			default:
				walkEnv(child, underContainers, out)
			}
		}
	case []any:
		for _, item := range v {
			walkEnv(item, underContainers, out)
		}
	}
}

func collectEnvValues(node any, out *[]candidate) {
	items, ok := node.([]any)
	if !ok {
		return
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		v, _ := m["value"].(string)
		if v != "" && looksLikeImageRef(v) {
			*out = append(*out, candidate{Ref: v, Source: lockfile.SourceEnv})
		}
	}
}

// extractFromConfigMap scans a ConfigMap's `data` map for image-like
// strings. Catches operator patterns where image defaults are published
// in a ConfigMap that the operator reads at runtime (either via envFrom
// or directly via the K8s API — both shapes are covered here). Random
// non-image ConfigMap data is filtered by crane.Digest validation.
func extractFromConfigMap(doc map[string]any) []candidate {
	if k, _ := doc["kind"].(string); k != "ConfigMap" {
		return nil
	}
	data, _ := doc["data"].(map[string]any)
	var out []candidate
	for _, v := range data {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if looksLikeImageRef(s) {
			out = append(out, candidate{Ref: s, Source: lockfile.SourceConfigMap})
		}
	}
	return out
}

// extractFromCRDSpec walks every string in a non-builtin kind's body for
// image-like patterns. The validation step (crane.Digest) is what makes
// this safe: random strings that happen to regex-match are dropped if the
// registry doesn't recognise them.
func extractFromCRDSpec(doc map[string]any) []candidate {
	var out []candidate
	walkAllStrings(doc, func(s string) {
		if looksLikeImageRef(s) {
			out = append(out, candidate{Ref: s, Source: lockfile.SourceCRDSpec})
		}
	})
	return out
}

func walkAllStrings(node any, fn func(string)) {
	switch v := node.(type) {
	case map[string]any:
		for _, child := range v {
			walkAllStrings(child, fn)
		}
	case []any:
		for _, item := range v {
			walkAllStrings(item, fn)
		}
	case string:
		fn(v)
	}
}
