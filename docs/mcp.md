# mcpd: Purgatorio as an MCP server

`mcpd` lets an LLM client drive a sandboxed Purgatorio session through the
Model Context Protocol (MCP). It is an ordinary guest command
(`/bin/mcpd.wasm`). It speaks MCP on its standard input and output, so an
MCP client starts it by starting `emu`. `mcpfs` goes the other way: it
mounts another MCP server's tools as files.

Both are ports of the Rust sibling's `mcpd` and `mcpfs` (its
`docs/mcp.md`, stages 1 to 5). The tools, their schemas, the protocol and
the sandbox are the same; the differences are listed at the end.

## Running it

```sh
go run ./tools/build
go build -o emu ./cmd/emu
./emu -r root mcpd
```

That command is a stdio MCP server. Register it with a client as a
command and its arguments. With Claude Code:

```sh
claude mcp add purgatorio -- /path/to/purgatorio-go/emu \
    -r /path/to/purgatorio-go/root mcpd
```

Clients that read an `mcpServers` table (Claude Desktop, `.mcp.json`)
take:

```json
{
  "mcpServers": {
    "purgatorio": {
      "command": "/path/to/purgatorio-go/emu",
      "args": ["-r", "/path/to/purgatorio-go/root", "mcpd"]
    }
  }
}
```

Use absolute paths: the client chooses the working directory. Add
`-vm interpreter` before `mcpd` to use wazero's interpreter.

Options, after `mcpd`:

- `-l logfile` appends the audit log to `logfile`, a name in the emu's
  own namespace (`-l /tmp/mcpd.log` is `root/tmp/mcpd.log` on the host).
  Without it nothing is logged. `mcpd` writes nothing else to its
  standard error, because on emu a guest's fd 2 is the console, which is
  the emu's standard output: the protocol stream. See "The audit log".
- `-n nsfile` grants the session more than the default, with a
  namespace(6) file. See below.
- `-t ms` is how long a file tool may take before it is abandoned
  (default 30000). See "Protocol".
- `-M bytes` is the most wasm memory all of a session's programs may hold
  between them (default `256m`; `k`, `m` and `g` multiply by 2^10, 2^20
  and 2^30). `-P progs` is how many programs a session may have at once
  (default 128). See "Limits".

You can try it by hand:

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"me","version":"0"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run","arguments":{"command":"echo hello; ns"}}}' \
  | ./emu -r root mcpd
```

## Protocol

`mcpd` follows MCP revision **2026-07-28**, stdio transport: JSON-RPC 2.0,
one message per line, nothing else on standard output. It exits when its
input ends.

- 2026-07-28 has no handshake. Each request carries
  `io.modelcontextprotocol/protocolVersion`,
  `io.modelcontextprotocol/clientInfo` (an object with a `name` and a
  `version`) and `io.modelcontextprotocol/clientCapabilities` in
  `params._meta`. `mcpd` answers `server/discover`, `tools/list`,
  `tools/call` and the three resource methods, and every result has
  `resultType`. A request without the metadata, or with a `clientInfo`
  that lacks either string, gets `-32602`; another version gets `-32022`
  with the supported list.
- A client of 2025-11-25, 2025-06-18, 2025-03-26 or 2024-11-05 sends
  `initialize` first. `mcpd` answers with the client's version if it is
  one of those, and with 2025-11-25 otherwise. After that, requests need
  no metadata. `ping` is answered for any client.
- `instructions` (in the `initialize` result and in `server/discover`) is
  a primer on Inferno sh, because models write bash by habit. The text is
  `appl/cmd/mcpd/primer.txt`; a test runs every example in it.
- Unknown methods get `-32601`, a line that is not JSON gets `-32700`, and
  an array (a batch) or a malformed request gets `-32600`. Notifications
  are never answered. An unknown tool is `-32602`. Bad arguments and
  failures of a tool are results with `isError: true`, so the model sees
  them. Their text is Inferno's error string, such as
  `/tmp/x: file does not exist`.
- Each file tool (`read_file`, `write_file`, `list_dir`, `stat`,
  `remove`, `ns`, `bind`, `unmount` and the resource methods) runs in a
  worker prog of its own, in the session's namespace. One that takes
  longer than `-t`, such as a read of a pipe nobody writes, fails with
  `timed out after 30000 ms ...`; its worker is killed, and the session,
  its files and its jobs go on. Only if the session prog itself stops
  answering for 15 seconds past a call's limit is it replaced as by
  `reset`.
- A client may send requests without waiting for answers. The session
  begins them in order, one at a time: a `run` or `mount` holds up the
  calls after it until it ends. The one exception is a `job_read` with
  `wait_ms`: it waits on its own, and the calls after it go ahead. Replies
  are sent in the order of the requests, again except a waiting
  `job_read`, which is answered as soon as it can be. A `reset` waits for
  the calls before it, except waiting `job_read`s, which it ends.
- `notifications/cancelled` is accepted and ignored; use `timeout_ms`.
- Prompts, resource subscriptions and server-to-client requests are not
  implemented.

## Tools

Every tool has a JSON Schema for its arguments and its result
(`tools/list` shows them; `appl/cmd/mcpd/tools.json` holds them, the
Rust `mcpd`'s list with two descriptions changed, see the end). A successful result is in
`structuredContent` and, as the same JSON, in a text block. File names
are absolute, or relative to `/tmp`.

| Tool | Arguments | Result |
|---|---|---|
| `run` | `command`, and optional `stdin`, `cwd`, `timeout_ms` (default 30000, at most 600000), `max_output_bytes` (default 65536 for each stream, at most 1 MiB) | `stdout`, `stderr`, `status` (Inferno's exit status string; empty is success), `ok`, `timed_out`, `stdout_truncated`, `stderr_truncated`, and `note` when the command ran into a limit |
| `read_file` | `path`, optional `offset`, `length` (default 65536, at most 1 MiB), `encoding` (`auto` or `base64`) | `content`, `encoding` (`utf8` or `base64`), `offset`, `bytes`, `size`, `eof` |
| `write_file` | `path`, `content`, optional `encoding` (`utf8` or `base64`), `mode` (`truncate`, `create` or `append`) | `bytes`, `size` |
| `list_dir` | `path` | `entries` (each `name`, `type`, `size`, `mode`, `append_only`, `mtime`), `truncated` |
| `stat` | `path` | the same fields, and `path`, `atime`, `uid`, `gid`, `muid` |
| `remove` | `path`, optional `recursive` | `removed` |
| `job_start` | `command` (an sh line) or `argv` (a program and its arguments, without sh), optional `stdin`, `cwd`, `timeout_ms` (no limit by default, at most a day) | `id`, `pid` |
| `job_read` | `id`, optional `offset`, `stderr_offset`, `wait_ms` (default 0, at most 600000), `max_bytes` (default 65536 for each stream, at most 1 MiB) | `stdout`, `stderr`, `offset`, `next_offset`, `stderr_offset`, `next_stderr_offset`, `lost`, `stderr_lost`, `running`, `status`, `ok`, and `note` when the job ran into a limit |
| `job_write` | `id`, optional `data`, `encoding` (`utf8` or `base64`), `eof` | `queued`, `eof` |
| `job_kill` | `id` | `killed` |
| `job_list` | none | `jobs` (each `id`, `pid`, `command`, `running`, `status`, `stdout_bytes`, `stderr_bytes`) |
| `ns` | none | `ns`: the namespace as `ns` prints it |
| `bind` | `from`, `to`, optional `order` (`replace`, `before`, `after`), `create` | `ok` |
| `mount` | `source` (a dial string or a `{command}` block), `to`, optional `spec`, `order`, `create`, `authenticate` (default true), `timeout_ms` (default 30000) | `ok` |
| `unmount` | `to`, optional `from` | `ok` |
| `reset` | none | `reset` |

Notes on `run`:

- The command line is run by `sh -c` in a new process group, with pipes
  for its three standard files. It starts in `cwd`, or `/tmp`. A
  launcher prog (`mcpd --exec`, shown by `ps` as `mcpd`) changes to that
  directory and starts sh, so a file server that hangs there holds up
  only this call, which its timeout ends. A `cwd` that does not exist is
  the call's error.
- The namespace belongs to the session (sh runs with `-n`, so it does
  not fork it), so `bind` and `mount` last until `reset`. Variables do
  not: sh copies its environment at start, as Inferno's does. Nor does
  the working directory.
- At the timeout the whole group is killed, and the call returns once the
  group is gone, with `timed_out` true and what was written so far.
- Output past the cap is read and dropped, so the command still runs to
  its end. Bytes that are not UTF-8 become U+FFFD; read binary data with
  `read_file`.
- A background program (`cmd &`) does not hold the call open. It keeps
  running until `reset` or the end of the session, but its output pipes
  close when the call returns. Use a job for such programs.

`read_file` returns text when the bytes are valid UTF-8 and base64
otherwise. A read that would end inside a character stops before it, so
reading a text file in pieces never splits one.

### Jobs

A job is a program the session keeps running between calls: a server, a
long build, an interactive program.

- `job_start` starts `sh -c command`, or `argv` directly (its first word
  is found as sh finds a command), in a new process group, in the
  session's namespace and environment, with pipes for its three standard
  files. It returns at once. At most 32 jobs run at a time.
- The session reads each job's stdout and stderr as they come, so a job
  never waits on output nobody has read. It keeps the last 1 MiB of each
  stream. Older output is dropped; a read that asks for it starts at the
  oldest byte kept and says how many bytes it missed in `lost` (or
  `stderr_lost`).
- Offsets count bytes from the start of a stream. Without `offset`, a
  `job_read` starts where the previous one ended, so repeated reads give
  new output only. A character cut at the end of what has come so far is
  left for the next read. Bytes that are not UTF-8 become U+FFFD.
- With `wait_ms`, `job_read` waits until there is output past the
  offsets, the job has exited, or the time is up. Other calls are
  answered meanwhile.
- `running` is true until the job has exited and its pipes are drained
  (programs it started may hold them open; the session stops reading 200
  ms after the exit). Then `status` is Inferno's exit status and `ok`
  says whether it is empty.
- `job_write` queues data for the job's input and returns at once; a
  helper prog writes it as the job reads, in order. `eof` closes the
  input after the data. Writing to a job that has exited, or after `eof`,
  fails.
- `job_kill` kills the job's process group. Its status is then `killed`.
  So does `timeout_ms`, after which `note` says it timed out.
- A job starts through the same launcher as `run`. A program that cannot
  be found or a `cwd` that does not exist ends the job at once, with
  status `fail:` and the reason, also on its standard error.
- Exited jobs stay in `job_list` and can be read until more than 32 have
  exited; the oldest go first. `reset` and the end of the session kill
  every job.

### Namespace tools

`ns`, `bind`, `mount` and `unmount` change the session's namespace as the
commands of the same names do, and the change lasts until `reset`. `ns`
reads the session prog's `/prog/n/ns` and tidies it as the `ns` command
does. `bind` and `unmount` are done by the session prog itself. `mount`
runs `/bin/mount` with the flags the arguments give, so a `{command}`
source (such as `{memfs -s}`) works as it does from sh.

The session gave up the right to attach devices before it served its
first call, so `bind '#C' /cmd` or `bind '#U' /n` fails with
`mount/attach disallowed`, whichever tool or command asks. A policy file
is the only way to grant a device.

### Resources

Files are also resources, with `purg://` URIs: `purg:///tmp/notes.txt` is
`/tmp/notes.txt` in the session's namespace. Bytes outside RFC 3986's
unreserved and sub-delimiter sets are percent-encoded. A URI always names
a file from the root, so it cannot name a device.

- `resources/list` lists the files under `/tmp`, at most 1000, in one
  page, with `name` (the file name) and `size`.
- `resources/templates/list` gives one template, `purg:///{+path}`, for
  any other file, such as `purg:///prog/1/status`.
- `resources/read` returns the whole file as `text` (`text/plain`) when it
  is valid UTF-8 and as a base64 `blob` (`application/octet-stream`)
  otherwise. A directory reads as its names, one per line, with `/` after
  each directory (`inode/directory`). A file longer than 1 MiB is refused
  with `-32603`; read it with `read_file`. A file that does not exist is
  `-32602` under 2026-07-28 and `-32002` under the earlier revisions,
  with the URI in `data`.
- Under 2026-07-28, list and read results carry `ttlMs: 0` and
  `cacheScope: "private"`; the template list is cacheable like
  `tools/list`.

### The audit log

With `-l logfile`, the front end appends one line per event:

```
1794334357 session start 3
1794334357 tool run ok 650ms {"command":"echo hi"}
1794334357 tool read_file error 1ms {"path":"/tmp/nope"}
1794334357 tool run invalid 0ms {"command":"x","timeout_ms":0}
1794334357 tool write_file ok 3ms {"path":"f","content":"yyyyyyyy...(5000 bytes)"}
1794334357 resource resources/read ok 8ms {"path":"/tmp/f"}
1794334357 session reset
1794334357 session end
```

The first field is seconds since 1970. A call's line is `tool` or
`resource`, its name, its result (`ok`; `error` when the tool failed or
the request got an error; `invalid` when the arguments did not fit the
schema), the time from receipt to reply, and the arguments as JSON with
every string longer than 64 characters cut to 48 and its length, and the
whole cut at 400 characters. Session lines are `session start <pid>`,
`session failed <why>`, `session reset` and `session end`.

The log is opened by the front end in the emu's namespace. The session
prog's own fd 2 is `/dev/null`, so the session cannot write to the log
through `/fd/2`, and the log's name is not in its namespace unless a
policy file binds it there.

## The sandbox

`mcpd` is three progs, a worker per file tool and a launcher per command
or job. The front end speaks the protocol, writes the log and touches no
file a tool names. A relay copies the client's input to a pipe, because
the front end must wait on the client and the session at once, and a
console mounted from another system (stage 3) cannot be waited on. The
front end starts a session prog with a copy of the namespace and the
environment and a new process group. It writes to the session only as
much as the session has acknowledged reading, so neither can block the
other with a large request and a large reply at once. The session prog
builds the namespace below, gives up the right to attach devices, and
only then serves tool calls. Every tool runs in that prog or in a prog it
starts, so no tool can name anything outside the session's namespace.

File tools run in a worker (`mcpd --file`) that the session prog starts
for each call, sharing its namespace and environment but none of its
file descriptors. The worker reads the request on its standard input and
writes the answer, followed by its length, as the last thing on its
standard output. A tool that writes `/fd/1` therefore writes into its own
worker's output, ahead of the real answer, and the session reads only
that answer and only its `result` or `error`. The session prog's own
pipes to the front end are not reachable from any tool or command, so a
session cannot forge a reply. The front end ends a session by killing
every prog in its domain, which it finds through `#pdomain=<session
pid>` (`docs/abi.md`): the session prog, its file servers, and
everything they started, whatever their process groups and whether or
not their parents still live.

### The session's `/prog`

`/prog` is `#pdomain`, bound by the session prog before it gives up
devices. It shows the session prog, its file servers, and every command,
job and worker it starts, with their descendants, in whatever process
group. Nothing else exists there: `ls /prog/<pid>`, `kill <pid>` and a
write to `/prog/<pid>/ctl` for the front end, its relay, another session
or any other prog fail with `file does not exist`. Under `NODEVS` the
session cannot attach `#p` again to see the whole table.

The default namespace:

| Name | What it is |
|---|---|
| `/` | A new `memfs` (in memory), holding only the names below. It becomes the root with `pctl(NEWNS)`, so the emu's root, and with it the host tree `#U`, is not in the namespace. |
| `/tmp` | A second `memfs`, private to the session and empty at the start. |
| `/bin`, `/lib` | The server's own `/bin` and `/lib`, mounted from `export -r`: a read-only export. |
| `/dev` | `null`, `random`, `time`, `user`, `sysname`, `memory` and `drivers` from `#c`. `cons` is `#c/null`. The real console, `consctl`, `kprint` and `sysctl` are absent. |
| `/env`, `/prog`, `/fd` | `#e` (the session's own copy of the environment), `#pdomain`, `#d`. |
| `/net`, `/chan`, `/n`, `/mnt` | Empty directories. |

After building it the session prog drops `#¤` from its namespace
(`nodev ¤` on its `ctl`, `docs/abi.md`) unless the policy file binds it,
and calls `pctl(NODEVS)`. From then on neither it nor anything it starts
can name a device with `#`, so `bind '#U' /n`, `'#C'` (host commands),
`'#I'` (the network) and `'#¤'` (the factotum) all fail with
`mount/attach disallowed`. `NODEVS` still allows `#e`, `#|`, `#M` and
`#s`, as in Inferno. `#s` is per prog domain (`docs/abi.md`), so
file2chan (`ramfile`) works inside a session.

What "read-only" means for `/bin` and `/lib`: the session reaches those
trees only through a 9P connection to an `export` started with the
`EXPRDONLY` flag (`export -r`; see `docs/abi.md`). That export answers
`file system read only` to an open for writing or truncation, and to
create, write, remove and wstat. The mount is also made without create
permission, so creating a file fails earlier with
`mounted directory forbids creation`. The check is in the kernel's export
code, not in the session, and the session has no other path to the files.

`reset` kills the session prog, its file servers, every command it ran
and their descendants, and starts a new session prog. The same happens
when the client closes the connection, and `mcpd` then exits.

Limits of the sandbox, stated plainly:

- A session can still kill its own progs, the session prog and its file
  servers among them. If it kills the session prog, the call in progress
  fails and a new session starts.
- The session prog still names a few files itself: `/bin/mcpd.wasm`
  when it starts a launcher or a worker, and `/prog/<pid>/ctl` and
  `/prog` to kill a command. A session that mounts a server that never
  answers on `/bin` or `/prog` can stall those, and then the front end
  replaces the session.
- Guest modules run with the engine's normal isolation.

## Granting more: `-n nsfile`

`mcpd -n nsfile` reads `nsfile` (a name in the emu's namespace), a
namespace file in the format of namespace(6), and for every session has
`nsbuild` apply it to the new namespace after it is built and before
`NODEVS`. At that point devices can still be attached. If the file cannot
be applied, no session is built and every tool call reports nsbuild's
error.

- The verbs are nsbuild's: `bind`, `mount`, `import`, `unmount`, `cd` and
  `.`, with their flags. Lines starting with `#` are comments. `$user`,
  `$sysname` and `$home` (`/tmp`) are set in the session's environment
  and expand in words.
- nsbuild runs in the session's namespace and reads the file from its
  standard input, so names in the file are names in the session's
  namespace, where `#U` is still the host tree.
- namespace(6) cannot make a directory, and the session's root holds only
  a few. So `mcpd` first makes every missing mount point named as the
  target of a `bind`, `mount` or `import` line in the file (not when the
  target names a variable).
- `fork`, `new`, `nodev` and `clear` change only nsbuild's own namespace
  and have no effect on the session.

A workspace from the host. On emu, `#U` is the directory given to `-r`,
so the workspace must be inside it (here `root/usr/work`):

```
# root/lib/mcp/work.ns: used as  emu -r root mcpd -n /lib/mcp/work.ns
bind -c '#U/usr/work' /n/work
```

The session can then read and write `/n/work`, and nothing else of the
host. To give it the network as well, add `bind '#I' /net`, and host
commands (`os`), which removes the sandbox for all practical purposes,
`bind '#C' /cmd`. A mount point can be bound whole: `bind /lib /n/lib`
shows the read-only `/lib`. The factotum, `#¤`, is the one device
`NODEVS` allows that the session loses; a policy grants it by binding it
anywhere (`bind '#¤' /n/factotum`).

## Remote sessions (stage 3): `rcmd host mcpd`

`rcmd` runs a command on another Purgatorio with this side's `/dev/cons`
as its console (Inferno rcmd(1)): `auxi/rstyxd` at the other end mounts
the caller's namespace on `/n/client` and opens `/n/client/dev/cons` for
the command's standard files. The same stdio server therefore runs
remotely with no transport of its own, and an emu on the client's host
is the bridge: the MCP client starts it, and it speaks MCP on its
standard input and output.

On the system that is to hold the sessions:

```sh
listen -A 'tcp!*!rstyx' auxi/rstyxd
```

On the host that runs the MCP client, the command to register is:

```sh
emu -r /path/to/root rcmd -A tcp!host!6668 mcpd
```

With Claude Code:

```sh
claude mcp add purgatorio-remote -- /path/to/purgatorio-go/emu \
    -r /path/to/purgatorio-go/root rcmd -A 'tcp!host!6668' mcpd
```

- `mcpd`'s options go after it: `rcmd -A tcp!host!6668 mcpd -t 10000 -n
  /lib/mcp/work.ns` (the file is a name on the remote system).
- Each connection is one `mcpd` with its own sessions. The caller's
  namespace on `/n/client` is in `rstyxd`'s namespace, not in a
  session's, unless its policy binds it.
- `rcmd`'s own errors go to the emu's standard output, which is the MCP
  stream, and the emu exits; the client sees the server go away.

**Authentication: not yet.** This repository's `rcmd` and `rstyxd` have
no keyring authentication or encryption yet (`rcmd` fails without `-A`,
and `rstyxd` must be run by `listen -A`), so a remote session today is
unauthenticated and in the clear: use it only on a trusted network or
loopback. The Rust sibling authenticates with its mount proof and then
encrypts with ChaCha20-Poly1305. Its formats and handshakes are now in
`pgauth` (`docs/auth.md`), but `rcmd`, `listen` and the kernel's export
do not use them yet. Once `rcmd` and `listen` authenticate, `mcpd` needs no
change: drop `-A` on both sides.

Test: `TestMcpRemote` in `cmd/emu/mcp_test.go` runs a session through
`rcmd -A` over loopback in one emu.

## Limits (stage 5)

A session is a prog domain (`#pdomain`, `docs/abi.md`): the session prog,
its two `memfs` and two `export -r`, and every launcher, command, job,
worker and helper it starts, through any number of generations and
whatever their process groups. Before it serves a call the session prog
sets two limits on that domain, which can only be lowered, so nothing in
the session can raise them:

- `-M bytes` (default 256 MiB): the wasm memory that the domain's progs
  may hold between them. `/tmp` is a `memfs`, a guest, so what is written
  there counts too. Every Go guest holds about 3 MiB, so an idle session
  (six progs) holds about 20 MiB and a `run` adds about 7 MiB for the
  launcher and sh.
- `-P progs` (default 128): how many progs the domain may have at once.
  An idle session has six; a `run` adds a launcher, sh and what sh
  starts; a file tool adds a worker for its length; a job adds a launcher
  and its programs.

Past a limit, what fails is the program that asked, and the result says
which limit it was:

- A program that tries to grow its memory past `-M` is refused the
  growth (`memory.grow` returns -1). A Go program then fails with
  `fatal error: out of memory`. `run` and `job_read` add a `note`, such
  as `out of memory: the session's programs may hold 128 MiB of memory
  between them (mcpd -M), and a program asked for more; it was stopped`.
- A spawn past `-P` fails with `no free procs`, and `note` names the
  limit. When mcpd itself cannot start the launcher or a worker, the call
  fails with `... no free procs (no free procs: the session may run at
  most 128 programs at once ...)`.
- A fork bomb (`fn bomb {bomb & bomb &}; bomb`) never has more than `-P`
  progs, and the session prog, its file servers and other sessions go on.
  Its programs keep each other going in the background until `reset`.

Not limited: CPU (each `run`, file tool, `mount` and job may have a
timeout, and `-P` bounds the busy threads), and kernel memory a session's
files use (`#e`, pipes, `#s` buffers).

## mcpfs: other MCP servers as files (stage 4)

```
mcpfs [-m mntpt] command [arg ...]
```

`command` is started with pipes for its standard input and output and
must speak MCP's stdio transport on them. It is any guest program: `mcpd`
for a local sandbox, or `rcmd -A host mcpd` for one on another system.
mcpfs makes the `initialize` handshake (revision 2025-11-25), lists the
tools (following `nextCursor`), and mounts on `mntpt` (default `/n/mcp`):

| Name | What it is |
|---|---|
| `server` | The server's `initialize` result as JSON: its `serverInfo`, `capabilities` and `instructions`. |
| `tools/NAME/` | A directory per tool. A name that cannot be a file name (empty, `.`, `..`, or with `/`) is left out. |
| `tools/NAME/schema` | The tool's definition from `tools/list` as JSON. |
| `tools/NAME/call` | Write the arguments, a JSON object, then read the result on the same open file. |

Each open of `call` is one call. Nothing written means `{}`. The first
read sends `tools/call` and returns the `CallToolResult` as one line of
JSON (`content`, `structuredContent`, `isError`). A tool that fails is a
result with `isError: true`, as MCP has it. Arguments that are not a JSON
object, and a request the server answers with a JSON-RPC error, fail the
read with the message. The result can be read from 0 or from where the
writes ended, so sh's read-write redirection works:

```sh
mcpfs mcpd
cat /n/mcp/tools/run/schema
{echo '{"command":"echo hello"}' >[1=0]; cat} <> /n/mcp/tools/run/call
```

`mcpfs` exits when the mount goes (`unmount`), and the server's input then
ends, so `mcpd` exits too. Test: `root/lib/test/sh/mcpfs.sh`, with `mcpd`
as the far end.

Not done, as in the Rust `mcpfs`: a host MCP server through `#C` (it
gives no way to write a command's input); resources, prompts and
notifications; more than one request at a time; a timeout on the server;
revision 2026-07-28 on the client side.

## Tests

- `cmd/emu/mcp_test.go` drives `emu mcpd` with JSON-RPC on the console:
  the handshake and the stateless revision, every tool, the sandbox
  (devices, `/bin` and `/lib`, `/prog`, forged replies), file tool
  timeouts, `-n`, `reset`, malformed requests, the primer's examples,
  resources, the audit log and stage 3 over `rcmd -A`, on both wazero
  modes.
- `root/lib/test/sh/mcp*.sh` run `mcpd` over a pipe inside one emu:
  `mcplimits.sh` covers `-M`, `-P`, the fork bomb and domain teardown,
  `mcpfs.sh` covers `mcpfs`.
- `appl/cmd/mcpd/rpc_test.go` and `sdk/mcp` test the protocol layer and
  the JSON on the build host.

## Differences from the Rust mcpd

- Command output and errors are this repository's commands', which
  follow Inferno: `cat: cannot open x: file does not exist` and status
  `fail:bad open` where the Rust `cat` says `cat: x: file does not
  exist` and `fail:errors`. A failed redirection ends sh's command line,
  as in Inferno.
- The read-only export answers exportfs's `file system read only`
  rather than `permission denied`.
- `/dev` has no `pid` (Inferno's `#c` has none).
- Memory: Go guests are larger, so `-M` must leave room (see Limits);
  the memory `note` recognizes the Go runtime's `fatal error: out of
  memory`.
- Variables set by one `run` are not seen by the next: Inferno's sh
  copies its environment (the Rust sh does not). The primer and the
  `run` description say so.
- The primer follows Inferno's sh where the Rust one differs: `"` is an
  operator, not an ordinary character; `&&` and `||` need `load std`;
  `exit` takes no status (`raise` does). Its command list names
  `gettar` and `puttar`; there is no `tar`.
- `tools.json` is the Rust `mcpd`'s tool list with two descriptions
  changed to match the above: `run` (variables) and `remove` (the error
  string).
- Stage 3 is unauthenticated until this repository's `rcmd` has keyring
  authentication (see above).
