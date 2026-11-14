# Self-hosting Go

Go's own compiler and linker run inside purgatorio-go as guests, so Go
programs, including the system's own commands, can be built in the emu.

```sh
go run ./tools/gotools      # compile.wasm, link.wasm and the library into root/
go run ./cmd/emu sh -c 'sh /lib/sh/gobuild /tmp/hello.wasm /tmp/hellodir; /tmp/hello.wasm'
```

`tools/gotools` builds `cmd/compile` and `cmd/link` for wasip1 into
`root/bin/go`, copies the standard library's packages, compiled for
wasip1, into `root/lib/go/pkg` with `root/lib/go/importcfg` naming them,
and puts the source of `sdk/sys` in `root/lib/go/src`. The defaults cover
fmt, os, strings and their dependencies; name more packages as arguments.

`/lib/sh/gobuild out dir` compiles `sdk/sys` from source and then the
package main in `dir`, and links them into `out`, all inside the emu with
the guests in `/bin/go`. There is no `go` command: it runs host programs
through os/exec, which wasip1 does not have, so the build is these two
steps, as `go build -x` would show them.

`TestSelfHost` builds `appl/cmd/echo` this way and runs the result; it is
skipped unless `tools/gotools` has run. CI runs it on the arm64 runner.

Building the standard library itself in the emu, rather than copying it,
is the next step: it needs `cmd/asm` for the runtime's assembly and a
driver that walks the import graph.
