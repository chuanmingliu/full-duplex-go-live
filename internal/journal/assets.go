package journal

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"regexp"
)

var localAsset = regexp.MustCompile(`(?:src|href)="([a-zA-Z0-9_.-]+\.(?:js|css))"`)

// Each HTML response pins its scripts and styles to their content, preventing
// mixed releases when a browser or proxy retains an older unversioned asset.
func versionedIndex(files fs.FS) ([]byte, error) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, err
	}
	var assetErr error
	index = localAsset.ReplaceAllFunc(index, func(attribute []byte) []byte {
		parts := localAsset.FindSubmatch(attribute)
		body, err := fs.ReadFile(files, string(parts[1]))
		if err != nil {
			assetErr = err
			return attribute
		}
		digest := sha256.Sum256(body)
		return []byte(fmt.Sprintf("%s?v=%x\"", attribute[:len(attribute)-1], digest[:8]))
	})
	return index, assetErr
}
