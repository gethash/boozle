// Package cachedir locates boozle's per-user cache directory, which holds
// the compiled PDFium module and overview thumbnails.
package cachedir

import (
	"os"
	"path/filepath"
)

// EnvOverride, when set, replaces the per-user cache root (used by tests and
// for hermetic or portable setups).
const EnvOverride = "BOOZLE_CACHE_DIR"

// Dir returns <user cache dir>/boozle/<sub>, creating it with 0700
// permissions (thumbnails can show confidential slides).
func Dir(sub string) (string, error) {
	root := os.Getenv(EnvOverride)
	if root == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(base, "boozle")
	}
	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
