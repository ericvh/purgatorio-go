// Package portcheck enforces the portability rule: the packages the
// bare-metal kernel will reuse must not reach the host operating system.
package portcheck

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/ericvh/purgatorio-go"

// Portable packages, relative to the module root.
var portable = []string{"abi", "styx", "platform", "core", "vm", "memdraw", "memdraw/defont"}

// Standard packages a portable package may not import directly. The Go
// runtime itself uses some of them; the rule is about our code.
var forbidden = map[string]bool{
	"os": true, "os/exec": true, "os/signal": true, "os/user": true,
	"net": true, "syscall": true, "path/filepath": true, "runtime/cgo": true,
	"plugin": true,
}

func TestPortable(t *testing.T) {
	var pkgs []string
	for _, p := range portable {
		pkgs = append(pkgs, module+"/"+p)
	}
	args := append([]string{"list", "-deps", "-f",
		"{{.ImportPath}}\t{{.Standard}}\t{{join .Imports \" \"}}"}, pkgs...)
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 3)
		path, std := f[0], f[1] == "true"
		if std {
			continue
		}
		if !strings.HasPrefix(path, module+"/") {
			t.Errorf("portable code depends on third-party %s", path)
			continue
		}
		if len(f) < 3 {
			continue
		}
		for _, imp := range strings.Fields(f[2]) {
			if forbidden[imp] {
				t.Errorf("%s imports %s", path, imp)
			}
		}
	}
}
