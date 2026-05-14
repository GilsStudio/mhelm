// Package insecure exposes a single env-gated "use plain HTTP" toggle
// for the mirror runtime. Useful when targeting a local OCI registry
// (e.g. registry:2 on localhost) that doesn't serve HTTPS. Production
// registries always serve HTTPS — leave the env var unset.
//
//	MHELM_INSECURE=1   plain HTTP + skip TLS verify across helm + crane
package insecure

import (
	"os"
	"strings"
)

// Enabled returns true when MHELM_INSECURE is set to a truthy value.
func Enabled() bool {
	switch strings.ToLower(os.Getenv("MHELM_INSECURE")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
