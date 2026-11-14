# Remote commands: rcmd, cpu, rstyxd, import

`rcmd host cmd` runs `cmd` on another machine with the caller's standard
input, output and error, the caller's namespace as `/n/client`, and the
command's exit status as `rcmd`'s. `cpu` is the same with the caller's
`/dev` bound before the remote one (`auxi/cpuslave` does the bind).
`import addr mtpt` mounts the Styx server at `addr`.

There are two protocols, chosen as every authenticated command chooses
(`docs/auth.md`, "Choosing algorithms": `-p`, `$authproto`,
`/lib/keys/proto`, `hybrid`):

- `hybrid` and `classical`: the Rust sibling's design (its
  `docs/rcmd.md`), with pgauth's handshake: `pgrstyx2` is the Rust
  protocol, byte for byte, and `pgrstyx3` the same with the version 4
  handshake.
- `keyring`: Inferno's rcmd(1), cpu(1), rstyxd(8) and import(4): the
  command line, then the connection is a 9P conversation in which the
  client exports its namespace, and the remote command uses
  `/n/client/dev/cons`. As in Inferno, `listen -p keyring` authenticates
  each caller with the keyring (`-k keyfile`, the ssl algorithms of
  `-a`, by default `none` and all of `#D`'s) and pushes the ssl the
  caller asks for before it runs `auxi/rstyxd`; `rcmd` (`-f keyfile`,
  `-e alg`) and `cpu` (`/usr/user/keyring/mach` or `default`, `-C alg`)
  authenticate with `auth->client`. `-A` is only for no authentication.
  `cmd/emu/keyringnet_test.go` runs them with the test keys in
  `/keydb`. Inferno's import is Plan 9's exportfs protocol, whose
  p9any authentication through factotum is not implemented.

`auxi/rstyxd` follows the protocol `listen` passes it in `authproto`:
`listen 'tcp!*!rstyx' auxi/rstyxd` serves `pgrstyx3`, `listen -p
classical ...` serves the Rust protocol, `listen -p hybrid,classical ...`
both, and `listen -A ...` serves pgauth's protocol without
authentication (`listen -A -p keyring ...`, Inferno's). A client never
falls back from one to the other; a server sent the version it does not
take answers `ERR hybrid authentication required` or `ERR classical
authentication required`.

## Commands

```
rcmd [-AS] [-k keyfile] [-e alg] [-x dir] [-p proto] host [cmd [arg...]]
cpu  [-AS] [-k keyfile] [-C alg] [-p proto] host [command [arg...]]
auxi/rstyxd [-A] [-e] [-k keyfile] [-p proto]
import [-AS] [-k keyfile] [-9 tree] [-p proto] addr mtpt
```

As in the Rust sibling: a bare host is `tcp!host!rstyx`; no command is
`sh`, one argument is a command line, several are quoted words; `-A`
skips authentication both ways and leaves the connection in the clear;
`-S` requires the server to prove it is the host dialled; `-k` names the
key to prove with (an authinfo or a seed), else the guest factotum or the
user's own key; `-e` and `-C` take `chacha20poly1305` (the default) or
`none`, which only goes with `-A`.

## Protocol

Frames are `kind u8 | length u32 LE | data`:

| kind | name | direction | data |
|---|---|---|---|
| 1 | HELLO | C to S | `pgrstyx2 <user>` (classical) or `pgrstyx3 <user>` (hybrid) |
| 2 | CHAL | S to C | the offer: 64 bytes (classical) or 1248 (hybrid); empty when no proof is wanted |
| 3 | PROOF | C to S | a version 3 or 4 proof for attach name `rstyx`, or empty |
| 4 | OK | S to C | the server's proof (version 3 or 4), or empty |
| 5 | ERR | S to C | the reason the client is refused |
| 6 | EXEC | C to S | `flags u8`, then the command line |
| 7 | STYX | both | bytes of the 9P conversation (the client's export) |
| 8 | IN | C to S | standard input; empty is end of file |
| 9 | OUT | S to C | standard output |
| 10 | ERRS | S to C | standard error |
| 11 | WIN | both | `stream u8, n u32 LE`: n more bytes of that stream may be sent |
| 12 | STATUS | S to C | the exit status; the last frame |

After an accepted proof, every frame from EXEC on is sealed as one
record of the mount's record layer, with the session's keys. IN, OUT and
ERRS start with 16 KiB of credit each. The server trusts clients as an
export in its namespace does; the client checks the server's proof for
the host it dialled. When the server proved that, the client's export
asks no proof of its own (EXEC flag 1) and runs in the clear inside the
encrypted connection; otherwise the export authenticates the server with
the same protocol, and the server answers from the same key.

Each end is one prog that owns the connection's write side and waits on
pipes (`purgatorio.wait`), with helper progs for the blocking reads: the
command itself with `--pump`, and on the server `--feed` for standard
input and `--session` for the mount of `/n/client` and the command,
which runs under `sh -c` in its own namespace and process group.

## Tests

`cmd/emu/rcmd_test.go` runs `rcmd` and `cpu` against `auxi/rstyxd`
under `listen` on the loopback, hybrid and classical, with standard
input and output, `/n/client`, `-S`, a refused protocol and a client
that gives no proof. `cmd/emu/pgauth_test.go` runs `import -S` against
`styxlisten`. `cmd/emu/cmds3_d_test.go` keeps Inferno's protocol
(`authproto=keyring`), and `cmd/emu/keyringnet_test.go` runs it with
keyring authentication: a foreign key and an algorithm the listener
does not allow are refused.
