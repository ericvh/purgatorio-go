// Command bench runs the benchmark suite of docs/perf.md: root/lib/bench/bench.sh
// inside the hosted emu, on each wazero mode, and in the kernel, and
// prints a table of the results in seconds, median and fastest.
//
//	go run ./tools/bench [-root root] [-vm compiler,interpreter] [-kernel] [-engines interpreter,aot,compiler] [-kvm] [-rounds 3]
//
// Every benchmark is timed inside the system, by the bench guest
// (appl/test/bench), so neither the emu's nor the kernel's start is in
// the numbers. The guests must be built first (go run ./tools/build); the
// kernel needs the TamaGo toolchain, as tools/kernel does.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func main() {
	vms := flag.String("vm", "compiler,interpreter", "wazero modes for the emu, separated by commas (empty for none)")
	kernel := flag.Bool("kernel", false, "also run the suite in the kernel")
	kvm := flag.Bool("kvm", false, "run the kernel under KVM")
	engines := flag.String("engines", "interpreter", "the kernel's engines, separated by commas: interpreter, aot, compiler")
	rounds := flag.Int("rounds", 3, "rounds of the suite in each system")
	root := flag.String("root", "root", "the emu's root (root-tinygo for guests built by tools/build -tinygo)")
	flag.Parse()
	var words []string
	for i := range *rounds {
		words = append(words, strconv.Itoa(i+1))
	}
	results := map[string]map[string][]float64{} // column -> benchmark -> samples
	var cols []string
	if *vms != "" {
		tmp, err := os.MkdirTemp("", "bench")
		check(err)
		defer os.RemoveAll(tmp)
		emu := filepath.Join(tmp, "emu")
		run(exec.Command("go", "build", "-o", emu, "./cmd/emu"))
		for _, m := range strings.Split(*vms, ",") {
			col := "emu-" + m
			fmt.Fprintf(os.Stderr, "bench: %s\n", col)
			out := run(exec.Command(emu, append([]string{"-r", *root, "-vm", m, "sh", "/lib/bench/bench.sh", "host"}, words...)...))
			results[col] = parse(out)
			cols = append(cols, col)
		}
	}
	if *kernel {
		// The kernel's /tmp is in its RAM tree, which cannot be written:
		// the file is packed into the root.
		big := filepath.Join(*root, "tmp", "bench.big")
		check(os.WriteFile(big, make([]byte, 100<<20), 0o644))
		defer os.Remove(big)
		for _, eng := range strings.Split(*engines, ",") {
			col := "kernel"
			args := []string{"run", "./tools/kernel", "-root", *root, "-cmds", "zeros,mount,unmount,export,test/bench",
				"-files", "tmp/bench.big",
				"-c", "sh /lib/bench/bench.sh kernel " + strings.Join(words, " ")}
			if *kvm {
				args = append(args, "-kvm")
				col = "kernel-kvm"
			}
			switch eng {
			case "interpreter":
			case "aot":
				args = append(args, "-aot")
			default:
				args = append(args, "-engine", eng)
			}
			if eng != "interpreter" {
				col += "-" + eng
			}
			fmt.Fprintf(os.Stderr, "bench: %s\n", col)
			results[col] = parse(run(exec.Command("go", args...)))
			cols = append(cols, col)
		}
	}
	var names []string
	for _, r := range results {
		for n := range r {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	fmt.Printf("| benchmark | %s |\n|---|%s\n", strings.Join(cols, " | "), strings.Repeat("---|", len(cols)))
	for _, n := range names {
		fmt.Printf("| %s |", n)
		for _, c := range cols {
			fmt.Printf(" %s |", summary(results[c][n]))
		}
		fmt.Println()
	}
}

// parse collects the "bench name seconds" lines of a run.
func parse(out []byte) map[string][]float64 {
	r := map[string][]float64{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(strings.TrimSpace(sc.Text()))
		if len(f) != 3 || f[0] != "bench" {
			continue
		}
		if v, err := strconv.ParseFloat(f[2], 64); err == nil {
			r[f[1]] = append(r[f[1]], v)
		}
	}
	return r
}

// summary is the median of the samples with the fastest in brackets.
func summary(s []float64) string {
	if len(s) == 0 {
		return "-"
	}
	s = slices.Clone(s)
	sort.Float64s(s)
	return fmt.Sprintf("%.3g (%.3g)", s[len(s)/2], s[0])
}

// run runs cmd and returns its standard output, failing if it fails.
func run(cmd *exec.Cmd) []byte {
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %s: %v\n%s", strings.Join(cmd.Args, " "), err, out.Bytes())
		os.Exit(1)
	}
	return out.Bytes()
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}
