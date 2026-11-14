package main

import "testing"

// TestLimits checks the memory limit on memory.grow (docs/abi.md, "Memory
// and prog limits"); limits.sh in the sh corpus covers the ctl messages,
// #pdomain and the limits at instantiation and spawn.
func TestLimits(t *testing.T) {
	const grown = "limit -\nlimit set\ngrow within the limit ok\ngrow past the limit -1\nload: /bin/lib/testmod.wasm: out of memory: virtual memory\n"
	cases := []struct {
		name        string
		args        []string
		out, status string
	}{
		{"grow", []string{"/bin/test/mem.wasm"}, grown, ""},
		{"allocate", []string{"sh", "-c", "/bin/test/mem.wasm hog >[2] /dev/null; echo status $status"},
			grown + "status 2\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emu(t, "", c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}
