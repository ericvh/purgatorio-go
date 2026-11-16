// Command kernel builds the native kernel with the TamaGo toolchain and
// boots it in QEMU's aarch64 virt machine.
//
//	go run ./tools/kernel [-root dir] [-kvm] [-smp n] [-build-only] [-o kernel.elf] [-c cmd]
//		[-cmds a,b] [-files a,b] [-disk image] [-netdev opts] [-engine spec | -aot [-classical]]
//		[-corpus | -net | -disk-check | -aot-check | -stress secs [-stress-progs n] [-stress-watch secs]]
//
// $TAMAGO names the TamaGo go command (tamago-go, GOOS=tamago); it
// defaults to ~/sdk/tamago-go/bin/go. With -kvm QEMU uses the host CPU
// (an aarch64 host with /dev/kvm); otherwise it emulates a Cortex-A53.
// The kernel powers the machine off when it is done, which ends QEMU.
//
// -engine chooses the kernel's WebAssembly engine (kernel/engine.go:
// interpreter, compiler, or aot). -aot compiles the root's modules ahead
// of time (tools/aot, on an arm64 host), packs the images beside them and
// boots with the engine "aot", trusting the public test key that signs
// them (hybrid; Ed25519 with -classical, and the engine "aot classical"). The kernel is built with the
// patch to wazero in third_party/wazero (patch.go).
//
// -smp gives the machine n CPUs. -corpus runs the sh script corpus
// against its goldens; -net and -disk-check boot with a network device
// or a scratch disk and check them from the host (see checks.go);
// -stress runs the corpus and the conformance guests in many progs at
// once for secs seconds, with a watchdog (see stress.go).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	kvm := flag.Bool("kvm", false, "run under KVM with the host CPU")
	buildOnly := flag.Bool("build-only", false, "build the kernel but do not boot it")
	out := flag.String("o", "kernel/kernel.elf", "kernel image to write")
	mem := flag.String("m", "2G", "memory for QEMU")
	smp := flag.Int("smp", 1, "CPUs for QEMU")
	initCmd := flag.String("c", "", "first command, as sh -c takes it (default: interactive sh -l)")
	corpus := flag.Bool("corpus", false, "run the sh script corpus (root/lib/test/sh) and compare with its goldens")
	rng := flag.Bool("rng", true, "give the machine a virtio entropy device")
	netdev := flag.String("netdev", "", "give the machine a virtio network device on QEMU user networking, with these extra -netdev user options (\"-\" for none)")
	cmds := flag.String("cmds", "", "more commands for the root, separated by commas")
	files := flag.String("files", "", "more files under root/ for the root, separated by commas")
	var disks multi
	flag.Var(&disks, "disk", "give the machine a virtio block device on this raw image (repeatable)")
	netCheck := flag.Bool("net", false, "check networking: TCP to and from the host over user networking")
	diskCheck := flag.Bool("disk-check", false, "check #S and the disk file system on a scratch image")
	stress := flag.Int("stress", 0, "run the corpus and the conformance guests concurrently for this many seconds")
	stressProgs := flag.Int("stress-progs", 8, "workers for -stress")
	stressWatch := flag.Int("stress-watch", 180, "seconds without a finished job before -stress's watchdog fails the run")
	engine := flag.String("engine", "", "the kernel's WebAssembly engine: interpreter, compiler or aot")
	withAOT := flag.Bool("aot", false, "compile the modules ahead of time and boot with the aot engine")
	root := flag.String("root", "root", "the root to pack (root-tinygo: docs/tinygo.md)")
	classical := flag.Bool("classical", false, "with -aot, sign images with the classical test key and take classical signatures")
	aotCheck := flag.Bool("aot-check", false, "check the aot engine's choice of images (implies -aot)")
	flag.Parse()
	if *aotCheck {
		*withAOT = true
	}
	if *withAOT && *engine == "" {
		*engine = "aot"
		if *classical {
			*engine = "aot classical"
		}
	}
	abs, err := filepath.Abs(*out)
	check(err)
	var chk checker
	monitor := []string{"-monitor", "none"}
	switch {
	case *stress > 0:
		sc := &stressChecker{secs: *stress, progs: *stressProgs, watch: time.Duration(*stressWatch) * time.Second,
			elf: abs, monitor: monitorSocket()}
		monitor = []string{"-monitor", "unix:" + sc.monitor + ",server,nowait"}
		chk = sc
	case *netCheck:
		chk = &netChecker{}
	case *diskCheck:
		chk = &diskChecker{}
	case *aotCheck:
		chk = aotChecker{}
	}
	if chk != nil {
		c := chk.setup()
		*initCmd, *netdev = c.init, c.netdev
		disks = append(disks, c.disks...)
		if *cmds != "" {
			c.cmds = append(c.cmds, *cmds)
		}
		*cmds = strings.Join(c.cmds, ",")
		if *files != "" {
			c.files = append(c.files, *files)
		}
		*files = strings.Join(c.files, ",")
	}
	var initrdArgs []string
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
	// The kernel embeds the root as kernel/initrd/root.tar.
	if *files != "" {
		initrdArgs = append([]string{"-files", *files}, initrdArgs...)
	}
	if *cmds != "" {
		initrdArgs = append([]string{"-cmds", *cmds}, initrdArgs...)
	}
	if *initCmd != "" {
		initrdArgs = append([]string{"-init", *initCmd}, initrdArgs...)
	}
	if *engine != "" {
		initrdArgs = append([]string{"-engine", *engine}, initrdArgs...)
	}
	if *root != "root" {
		initrdArgs = append([]string{"-root", *root}, initrdArgs...)
	}
	if *withAOT {
		initrdArgs = append([]string{"-aot"}, initrdArgs...)
		if *classical {
			initrdArgs = append([]string{"-classical"}, initrdArgs...)
		}
	}
	initrd := exec.Command("go", append([]string{"run", "./tools/initrd"}, initrdArgs...)...)
	initrd.Stdout, initrd.Stderr = os.Stdout, os.Stderr
	check(initrd.Run())
	ldflags := "-T 0x40010000 -R 0x1000"
	env := append(os.Environ(), "GOOS=tamago", "GOARCH=arm64",
		"GOOSPKG=github.com/usbarmory/tamago", "GOTELEMETRY=off")
	check(patchWazero(env))
	build := exec.Command(tamago, "build", "-trimpath", "-ldflags", ldflags, "-o", abs, "./cmd/kernel")
	build.Dir = "kernel"
	build.Env = env
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	check(build.Run())
	if *buildOnly {
		return
	}
	args := append([]string{"-M", "virt,gic-version=3", "-m", *mem, "-nographic"}, monitor...)
	args = append(args, "-serial", "stdio", "-kernel", abs, "-smp", fmt.Sprint(*smp))
	if *kvm {
		args = append(args, "-enable-kvm", "-cpu", "host")
	} else {
		args = append(args, "-cpu", "cortex-a53")
	}
	args = append(args, devArgs(*rng, *netdev, disks)...)
	q := exec.Command("qemu-system-aarch64", args...)
	q.Stderr = os.Stderr
	if chk != nil {
		st := chk.run(q)
		if sc, ok := chk.(*stressChecker); ok {
			os.Remove(sc.monitor)
		}
		os.Exit(st)
	}
	q.Stdin = os.Stdin
	if !*corpus {
		q.Stdout = os.Stdout
		check(q.Run())
		return
	}
	console, err := q.Output()
	check(err)
	os.Exit(compare(string(console), scripts))
}

// devArgs are QEMU's arguments for the virtio devices. The kernel drives
// the modern virtio-mmio interface, which QEMU gives only on request.
func devArgs(rng bool, netdev string, disks []string) []string {
	args := []string{"-global", "virtio-mmio.force-legacy=false"}
	if rng {
		args = append(args, "-device", "virtio-rng-device")
	}
	for i, d := range disks {
		id := fmt.Sprintf("disk%d", i)
		args = append(args, "-drive", "if=none,format=raw,file="+d+",id="+id, "-device", "virtio-blk-device,drive="+id)
	}
	if netdev != "" {
		nd := "user,id=net0"
		if netdev != "-" {
			nd += "," + netdev
		}
		args = append(args, "-netdev", nd, "-device", "virtio-net-device,netdev=net0")
	}
	return args
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

// marker starts each script's output in a corpus run.
const marker = "@@ corpus @@ "

// compare splits a corpus run's console output by script and checks each
// part against the script's golden output, as the emu's corpus test does.
func compare(out string, scripts []string) int {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "kernel: engine") {
			fmt.Println(line)
		}
	}
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
