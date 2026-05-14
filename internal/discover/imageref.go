package discover

import (
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"sigs.k8s.io/yaml"
)

// looksLikeImageRef is a fast filter — `name.ParseReference` is permissive
// (it accepts `foo:bar` as image `foo` tag `bar`), so we add structural
// guards. Real image refs in operator env vars / CRD specs nearly always
// include a registry path (a `/`), so requiring that filters most
// false positives without missing real refs.
func looksLikeImageRef(s string) bool {
	if len(s) < 5 || len(s) > 256 {
		return false
	}
	if strings.ContainsAny(s, " \t\n\r\"'<>{}[]()&|`$") {
		return false
	}
	if !strings.Contains(s, "/") {
		return false
	}
	if _, err := name.ParseReference(s); err != nil {
		return false
	}
	return true
}

// parseDocs splits each rendered template file on YAML document separators
// and unmarshals each non-empty doc. Unparseable docs are silently dropped
// (helm hooks may produce non-YAML output).
func parseDocs(rendered map[string]string) []map[string]any {
	var out []map[string]any
	for _, content := range rendered {
		for _, doc := range strings.Split(content, "\n---") {
			doc = strings.TrimSpace(doc)
			if doc == "" {
				continue
			}
			var m map[string]any
			if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
				continue
			}
			if m == nil {
				continue
			}
			out = append(out, m)
		}
	}
	return out
}

// builtinAPIVersions are K8s API groups whose resources we treat as
// "standard" — anything else is considered a CRD and walked for image refs
// during the CRD-spec extraction pass.
var builtinAPIVersions = map[string]bool{
	"v1":                              true, // core
	"apps/v1":                         true,
	"batch/v1":                        true,
	"batch/v1beta1":                   true,
	"extensions/v1beta1":              true,
	"networking.k8s.io/v1":            true,
	"rbac.authorization.k8s.io/v1":    true,
	"policy/v1":                       true,
	"policy/v1beta1":                  true,
	"storage.k8s.io/v1":               true,
	"autoscaling/v1":                  true,
	"autoscaling/v2":                  true,
	"autoscaling/v2beta1":             true,
	"autoscaling/v2beta2":             true,
	"scheduling.k8s.io/v1":            true,
	"node.k8s.io/v1":                  true,
	"discovery.k8s.io/v1":             true,
	"coordination.k8s.io/v1":          true,
	"admissionregistration.k8s.io/v1": true,
	"apiextensions.k8s.io/v1":         true,
	"certificates.k8s.io/v1":          true,
	"events.k8s.io/v1":                true,
}

func isBuiltinKind(doc map[string]any) bool {
	apiVersion, _ := doc["apiVersion"].(string)
	return builtinAPIVersions[apiVersion]
}
