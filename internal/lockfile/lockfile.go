package lockfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

const SchemaVersion = 1

type File struct {
	SchemaVersion int        `json:"schemaVersion"`
	Chart         Chart      `json:"chart"`
	Upstream      Upstream   `json:"upstream"`
	Downstream    Downstream `json:"downstream"`
	Mirror        Mirror     `json:"mirror"`
}

type Chart struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Upstream struct {
	Type               string `json:"type"`
	URL                string `json:"url"`
	ChartContentDigest string `json:"chartContentDigest"`
	OCIManifestDigest  string `json:"ociManifestDigest,omitempty"`
}

type Downstream struct {
	Ref               string `json:"ref"`
	OCIManifestDigest string `json:"ociManifestDigest"`
}

type Mirror struct {
	Tool      string    `json:"tool"`
	Version   string    `json:"version"`
	Timestamp time.Time `json:"timestamp"`
}

// ContentDigest returns the sha256 of b prefixed with "sha256:".
func ContentDigest(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// HexFromDigest strips the "sha256:" prefix, returning raw hex (matches the
// form Helm's index.yaml uses for chart digests).
func HexFromDigest(d string) string {
	return strings.TrimPrefix(d, "sha256:")
}

func Write(path string, f File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Read loads an existing chart-lock.json. Returns os.ErrNotExist when the
// file does not exist — callers may use errors.Is to treat that as
// "fresh start".
func Read(path string) (File, error) {
	var f File
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	return f, nil
}

// IsNotExist reports whether err signals a missing lockfile (sugar for
// errors.Is(err, os.ErrNotExist)).
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
