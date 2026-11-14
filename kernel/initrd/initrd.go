// Package initrd holds the kernel's root file system, packed by
// go run ./tools/initrd as root.tar.gz.
package initrd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"

	_ "embed"

	"github.com/ericvh/purgatorio-go/core"
)

//go:embed root.tar.gz
var root []byte

// Load adds the root's files to t.
func Load(t *core.StaticTree) error {
	zr, err := gzip.NewReader(bytes.NewReader(root))
	if err != nil {
		return err
	}
	r := tar.NewReader(zr)
	for {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if h.Typeflag == tar.TypeDir {
			t.AddDir(name)
			continue
		}
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		t.AddFile(name, b)
	}
}
