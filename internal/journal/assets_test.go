package journal

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func TestVersionedIndexPinsAssetContents(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte(`<link href="app.css"><script src="app.js"></script>`)}, "app.css": {Data: []byte("body{}")}, "app.js": {Data: []byte("old")}}
	first, err := versionedIndex(files)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("old"))
	if !strings.Contains(string(first), fmt.Sprintf("app.js?v=%x", digest[:8])) {
		t.Fatal(string(first))
	}
	files["app.js"].Data = []byte("new")
	second, err := versionedIndex(files)
	if err != nil || string(first) == string(second) {
		t.Fatal("asset change did not invalidate URL", err)
	}
	delete(files, "app.js")
	if _, err = versionedIndex(files); err == nil {
		t.Fatal("missing asset accepted")
	}
}
