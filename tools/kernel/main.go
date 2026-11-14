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
)

func main() {
	kvm := flag.Bool("kvm", false, "run under KVM with the host CPU")
	buildOnly := flag.Bool("build-only", false, "build the kernel but do not boot it")
	out := flag.String("o", "kernel/kernel.elf", "kernel image to write")
	mem := flag.String("m", "1G", "memory for QEMU")
	flag.Parse()

	tamago := os.Getenv("TAMAGO")
	if tamago == "" {
		home, _ := os.UserHomeDir()
		tamago = filepath.Join(home, "sdk", "tamago-go", "bin", "go")
	}
	abs, err := filepath.Abs(*out)
	check(err)
	build := exec.Command(tamago, "build", "-trimpath", "-ldflags", "-T 0x40010000 -R 0x1000", "-o", abs, "./cmd/kernel")
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
	q.Stdin, q.Stdout, q.Stderr = os.Stdin, os.Stdout, os.Stderr
	check(q.Run())
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "kernel:", err)
		os.Exit(1)
	}
}
