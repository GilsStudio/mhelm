package chartfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
)

const (
	TypeRepo = "repo"
	TypeOCI  = "oci"
)

type Endpoint struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type File struct {
	Upstream   Endpoint `json:"upstream"`
	Downstream Endpoint `json:"downstream"`
}

func Load(filePath string) (File, error) {
	var f File
	b, err := os.ReadFile(filePath)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", filePath, err)
	}
	return f, nil
}

func (f File) Validate() error {
	switch f.Upstream.Type {
	case TypeRepo:
		if f.Upstream.Name == "" {
			return fmt.Errorf("upstream.name is required when upstream.type=%q", TypeRepo)
		}
	case TypeOCI:
		if !strings.HasPrefix(f.Upstream.URL, "oci://") {
			return fmt.Errorf("upstream.url must start with oci:// when upstream.type=%q", TypeOCI)
		}
	case "":
		return fmt.Errorf("upstream.type is required (%q or %q)", TypeRepo, TypeOCI)
	default:
		return fmt.Errorf("upstream.type %q invalid (expected %q or %q)", f.Upstream.Type, TypeRepo, TypeOCI)
	}
	if f.Upstream.URL == "" {
		return fmt.Errorf("upstream.url is required")
	}
	if f.Upstream.Version == "" {
		return fmt.Errorf("upstream.version is required")
	}
	if f.Downstream.Type != TypeOCI {
		return fmt.Errorf("downstream.type must be %q (got %q)", TypeOCI, f.Downstream.Type)
	}
	if !strings.HasPrefix(f.Downstream.URL, "oci://") {
		return fmt.Errorf("downstream.url must start with oci://")
	}
	return nil
}

// ChartName returns the chart name for the push reference: Upstream.Name for
// repo-type, last path segment of Upstream.URL for oci-type.
func (f File) ChartName() string {
	if f.Upstream.Type == TypeRepo {
		return f.Upstream.Name
	}
	ref := strings.TrimPrefix(f.Upstream.URL, "oci://")
	return path.Base(ref)
}
