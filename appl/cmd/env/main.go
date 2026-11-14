// Command env prints the environment variables, one name=value per
// line, in directory order (Inferno env(1)).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// getenv reads a variable as Env->getenv does: the whole file, raw.
func getenv(name string) string {
	f, err := os.Open("/env/" + name)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	buf := make([]byte, st.Size())
	n, _ := f.Read(buf)
	return string(buf[:max(n, 0)])
}

// names lists /env without duplicates; an unreadable /env lists nothing,
// as with Readdir.
func names() []string {
	fd, err := sys.Open("/env", sys.OREAD)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return nil
		}
		if len(ds) == 0 {
			return out
		}
		for _, d := range ds {
			if !seen[d.Name] {
				seen[d.Name] = true
				out = append(out, d.Name)
			}
		}
	}
}

func main() {
	if len(os.Args) > 1 {
		os.Stderr.WriteString("Usage: env\n")
		sys.Exits("fail:usage")
	}
	for _, n := range names() {
		os.Stdout.WriteString(n + "=" + getenv(n) + "\n")
	}
}
