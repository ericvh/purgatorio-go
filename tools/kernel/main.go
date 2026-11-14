// Command kernel builds the native kernel with the TamaGo toolchain and
// boots it in QEMU's aarch64 virt machine.
//
//	go run ./tools/kernel [-kvm] [-build-only] [-o kernel.elf]
//
// $TAMAGO names the TamaGo go command (tamago-go, GOOS=tamago); it
// defaults to ~/sdk/tamago-go/bin/go. With -kvm QEMU uses the host CPU
// (an aarch64 host with /dev/kvm); otherwise it emulates a Cortex-A53.
// The kernel powers the machine off when it is done, which ends QEMU.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	kvm := flag.Bool("kvm", false, "run under KVM with the host CPU")
	buildOnly := flag.Bool("build-only", false, "build the kernel but do not boot it")
	out := flag.String("o", "kernel/kernel.elf", "kernel image to write")
	mem := flag.String("m", "2G", "memory for QEMU")
	initCmd := flag.String("c", "", "first command, as sh -c takes it (default: interactive sh -l)")
	corpus := flag.Bool("corpus", false, "run the sh script corpus (root/lib/test/sh) and compare with its goldens")
	flag.Parse()
	initrdArgs := []string{}
	var scripts []string
	if *corpus {
		var err error
		scripts, err = filepath.Glob("root/lib/test/sh/*.sh")
		check(err)
		var cmd strings.Builder
		for _, sc := range scripts {
			name := strings.TrimSuffix(filepath.Base(sc), ".sh")
			fmt.Fprintf(&cmd, "echo '%s%s'; unmount /mnt >[2] /dev/null; memfs /mnt; mkdir /mnt/shtest; sh /lib/test/sh/%s.sh; ", marker, name, name)
		}
		fmt.Fprintf(&cmd, "echo '%send'", marker)
		*initCmd = cmd.String()
		initrdArgs = nil // lib and the default commands
	}

	tamago := os.Getenv("TAMAGO")
	if tamago == "" {
		home, _ := os.UserHomeDir()
		tamago = filepath.Join(home, "sdk", "tamago-go", "bin", "go")
	}
	abs, err := filepath.Abs(*out)
	check(err)
	// The kernel embeds the root as kernel/initrd/root.tar.
	if *initCmd != "" {
		initrdArgs = append([]string{"-init", *initCmd}, initrdArgs...)
	}
	initrd := exec.Command("go", append([]string{"run", "./tools/initrd"}, initrdArgs...)...)
	initrd.Stdout, initrd.Stderr = os.Stdout, os.Stderr
	check(initrd.Run())
	ldflags := "-T 0x40010000 -R 0x1000"
	build := exec.Command(tamago, "build", "-trimpath", "-ldflags", ldflags, "-o", abs, "./cmd/kernel")
	build.Dir = "kernel"
	build.Env = append(os.Environ(), "GOOS=tamago", "GOARCH=arm64",
		"GOOSPKG=github.com/usbarmory/tamago", "GOTELEMETRY=off")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	check(build.Run())
	if *buildOnly {
		return
	}
	args := []string{"-M", "virt,gic-version=3", "-m", *mem, "-nographic", "-monitor", "none",
		"-serial", "stdio", "-kernel", abs}
	if *kvm {
		args = append(args, "-enable-kvm", "-cpu", "host")
	} else {
		args = append(args, "-cpu", "cortex-a53")
	}
	q := exec.Command("qemu-system-aarch64", args...)
	q.Stdin, q.Stderr = os.Stdin, os.Stderr
	if !*corpus {
		q.Stdout = os.Stdout
		check(q.Run())
		return
	}
	console, err := q.Output()
	check(err)
	os.Exit(compare(string(console), scripts))
}

// marker starts each script's output in a corpus run.
const marker = "@@ corpus @@ "

// compare splits a corpus run's console output by script and checks each
// part against the script's golden output, as the emu's corpus test does.
func compare(out string, scripts []string) int {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	parts := map[string]string{}
	sections := strings.Split(out, marker)
	for _, sec := range sections[1:] {
		name, body, _ := strings.Cut(sec, "\n")
		parts[name] = body
	}
	if _, ok := parts["end"]; !ok {
		fmt.Fprintln(os.Stderr, "kernel: the corpus run did not finish; output:\n"+out)
		return 1
	}
	failed := 0
	for _, sc := range scripts {
		name := strings.TrimSuffix(filepath.Base(sc), ".sh")
		want, err := os.ReadFile(strings.TrimSuffix(sc, ".sh") + ".out")
		check(err)
		if parts[name] != string(want) {
			failed++
			fmt.Printf("FAIL %s\ngot:\n%s\nwant:\n%s\n", name, parts[name], want)
		} else {
			fmt.Printf("ok   %s\n", name)
		}
	}
	fmt.Printf("%d of %d scripts passed\n", len(scripts)-failed, len(scripts))
	if failed > 0 {
		return 1
	}
	return 0
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "kernel:", err)
		os.Exit(1)
	}
}
