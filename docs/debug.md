# Debugging tools

Inferno's debugging commands read `/prog/n` (`#p`) and `/prof` (`#P`).
The ports here read the same files, as docs/plan.md 5.1 and 5.2
describe them for wasm guests. Guests are wasm, not Dis, so a pc is a
wasm function index and a module is a `.wasm` file.

| command | Inferno's | what it does |
|---|---|---|
| `stack [-v] pid` | `stack` | prints a prog's call stack at its current host call |
| `broke` | `appl/tiny/broke.b` | kills the broken progs and prints their pids |
| `trace [-v] cmd [arg ...]` | (Rust sibling) | runs a command and prints each host call it makes |
| `mdb [-w] file [cmd]` | `mdb` | binary file editor; its `$` commands describe a wasm module |
| `cprof [-fne] [-m mod]... cmd [arg ...]` | `cprof` | which functions a command called, and how often |
| `mprof [-bcMflnve] [-m mod]... [cmd arg ...]` | `mprof` | how much memory each function made the guest grow |
| `wasmdump [-hsiemcf] file ...` | `disdump` | prints a module's sections, imports, exports and functions |
| `wasmdep file ...` | `disdep` | prints a module's ABI version and imports |

`prof` is in docs/plan.md 5.2.

## Function names

A Go guest is linked without a wasm name section, but its pclntab (the
table the Go runtime uses for tracebacks) is in its data.
`sdk/wasmobj` finds it and names each wasm function with its Go
function, file and first line, as Inferno's tools read a `.sbl` file.
A module that is not Go is named from its name section, else from its
exports and imports. There is no table from a wasm pc to a source line,
so every tool reports a function at the line it starts.

## stack

```
% stack 7
unknown fn() Module $purgatorio PC 22
github.com/ericvh/purgatorio-go/sdk/sys.sleep() github.com/ericvh/purgatorio-go/sdk/sys/sys_wasip1.go:80
github.com/ericvh/purgatorio-go/sdk/sys.Sleep() github.com/ericvh/purgatorio-go/sdk/sys/sys_wasip1.go:297
main.main() github.com/ericvh/purgatorio-go/appl/cmd/sleep/main.go:28
runtime.main() runtime/proc.go:153
...
```

A host function's frame is "unknown fn()" in the module
`$wasi_snapshot_preview1` or `$purgatorio`, as Inferno shows `$Sys`. A
prog has a stack only while it is in a host call; for one running guest
code the read fails with `thread must be stopped`. Inferno's `-v`
prints arguments and locals, which a guest has no table for, so `-v`
here adds each frame's function index, code offset and module. `-p`
(where to find `.sbl` files) is not needed: the symbols are in the
module.

## broke

A trap ends a prog here instead of leaving it broken, so `broke`
usually finds nothing. It is kept for progs that do report `broken`.

## trace

`trace` holds the command at each reported host call with
`/prog/n/dbgctl`'s `step`, which here runs to the next host call, and
prints the innermost guest frame:

```
% trace echo hi
3: syscall.fd_fdstat_set_flags() syscall/fs_wasip1.go:292
...
3: syscall.fd_write() syscall/fs_wasip1.go:225
hi
```

With `-v` the whole stack follows each line. To see every call from
the start, `trace` spawns a copy of itself that waits until trace has
attached and then execs the command in the same prog. Only the
command's prog is followed. When the command ends with a status that is
not a `fail:` one (a trap or a Go panic), trace prints `trace: pid
"name": broken: status`. The Rust sibling's `trace` turns on stack
keeping, which every prog has here.

## mdb

Inferno's `mdb` is a binary file editor, not a prog debugger, and so is
this one: addresses, counts, the `/`, `?` and `=` formats and `/w`,
`/W` behave as in mdb(1). The `$` commands that decode a Dis file
decode a wasm module instead: `$h` header, `$i` imports, `$l` exports,
`$D` function types, `$d` data segments; `?i` lists functions from
index dot. `$H` and `$s` print nothing.

## cprof and mprof

Both drive `#P` through `sdk/profile`, the counterpart of Inferno's
Profile module. `cprof` uses `startcp` on its own pid before it spawns
the command, since the command starts running as soon as it is
spawned; `#P` then counts in the modules the command and its
descendants start. It marks each function called (`+`) or not (`-`);
with `-f` it gives the count. A Go function is entered again when its
goroutine resumes, so a count can exceed the calls. Inferno's `-r`
(results kept in `.prf` files) and the form with no command are not
provided.

`mprof` uses `startmp`. The guest's allocator is inside the guest, so
what is counted is the growth of linear memory while each function ran,
including what it called. The outermost functions and the runtime's
allocation paths carry the most. `-l` is taken as `-f`.

## wasmdump and wasmdep

The output follows the Rust sibling's. `wasmdump -f` lists every
function with its index, body size, type and name. `wasmdep` prints
`abi <version>` and `import <module> <name>` per import; for a module
that imports `load`, `spawn_prog` or `exec` it adds `refers <path>` for
each absolute path ending in `.wasm` in its data, a guess from its
strings. Inferno's `disdep` follows a tree of Dis modules; a wasm
module imports host functions, so there is no tree to follow.
