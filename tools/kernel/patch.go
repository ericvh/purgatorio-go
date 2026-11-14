package main

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// patchDir holds the kernel's patch to wazero (its README.md): files
	// that replace or join wazero's.
	patchDir = "third_party/wazero"
	// patchedDir is the patched copy of wazero the kernel's go.mod
	// points to. It is made from the module cache at every build.
	patchedDir = "kernel/.wazero"
)

// patchWazero makes the patched copy of wazero the kernel builds with:
// wazero's Go files from the module cache, less tests and examples, with
// the patch's files over them. It refuses when wazero is not the
// version the patch was made for, or a file it replaces has changed.
func patchWazero(env []string) error {
	q := exec.Command("go", "mod", "download", "-json", "github.com/tetratelabs/wazero@"+wazeroVersion())
	q.Env, q.Stderr = env, os.Stderr
	out, err := q.Output()
	if err != nil {
		return err
	}
	var dir string
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), `"Dir": "`); ok {
			dir = strings.TrimSuffix(v, `",`)
		}
	}
	if dir == "" {
		return fmt.Errorf("no wazero module")
	}
	f, err := os.Open(filepath.Join(patchDir, "UPSTREAM"))
	if err != nil {
		return err
	}
	defer f.Close()
	want := map[string]string{} // replaced file -> its upstream SHA-256
	version := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "version "); ok {
			version = v
		} else if sum, path, ok := strings.Cut(line, "  "); ok {
			want[path] = sum
		}
	}
	if version != wazeroVersion() {
		return fmt.Errorf("the wazero patch is for %s, the kernel requires %s: port %s", version, wazeroVersion(), patchDir)
	}
	if err := os.RemoveAll(patchedDir); err != nil {
		return err
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			switch filepath.Base(rel) {
			case "testdata", "examples", "cmd", "site", "integration_test", ".github":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, "_test.go") ||
			!(strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, ".s") || rel == "go.mod" || rel == "LICENSE" || rel == "NOTICE") {
			return nil
		}
		return copyFile(p, filepath.Join(patchedDir, rel))
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(patchDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		rel, _ := filepath.Rel(patchDir, p)
		up := filepath.Join(dir, rel)
		if b, err := os.ReadFile(up); err == nil {
			if sum := fmt.Sprintf("%x", sha256.Sum256(b)); sum != want[filepath.ToSlash(rel)] {
				return fmt.Errorf("wazero's %s is not the file %s patches: port it", rel, patchDir)
			}
		} else if want[filepath.ToSlash(rel)] != "" {
			return fmt.Errorf("wazero has no %s", rel)
		}
		return copyFile(p, filepath.Join(patchedDir, rel))
	})
}

// wazeroVersion is the wazero the kernel's go.mod requires.
func wazeroVersion() string {
	b, err := os.ReadFile("kernel/go.mod")
	check(err)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "github.com/tetratelabs/wazero" {
			return f[1]
		}
	}
	return ""
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}
