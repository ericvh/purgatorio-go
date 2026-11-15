# mash and fs

Both follow Inferno's sources: `appl/cmd/mash` for mash(1),
mash-make(1) and the history module, and `appl/cmd/fs.b` with
`appl/cmd/fs/*.b` and `appl/lib/fslib.b` for fs(1). The tests are the
corpus scripts `root/lib/test/sh/mash.sh` (which runs the mash scripts
in `root/lib/test/mash`) and `root/lib/test/sh/fs.sh`, and `TestMash`
in `cmd/emu/mash_test.go`.

## mash

The parser is hand-written recursive descent for the grammar of
`mash.y`, with its precedences and its error recovery: a syntax error is
reported unless another came within the last three tokens, and the rest
of the input line is discarded. Commands at top level run as soon as
their terminator is read. As in Inferno:

- A newline ends a command only at the console. In a file or a `-c`
  string commands end with `;` or `&`, or at the end of the input.
  Keywords (`in`, `hd`, `for`, ...) are keywords anywhere, so
  `echo in` is a syntax error.
- An error in the last command of a file or string, the one ended by the
  end of the input, is reported without the file and line.
- mash keeps no `$status`. Under `-e` a command it waited for that fails
  ends mash with the command's status as it is; only the last command of
  a pipeline is waited for. A prog that dies other than by exiting or
  failing has its wait message printed, and mash goes on. A builtin that
  fails (`make` included) ends mash only under `-e`. mash itself exits
  `fail: mash usage`, `fail: mash error` (a reported error under `-e`),
  `fail: mash system error`, or under `-e` `fail: make error`.
- `exit` ends mash when it runs at top level; inside `eval`, `@{...}`,
  a pipeline or the background it ends only that.
- The first dependency (`:`) clears the rules (`:~`) given before it,
  unless `make -clear` has run, as Inferno's mashfile does first.

What differs:

- Builtins run in mash, one after another; Inferno's run each in a
  thread. A builtin in a pipeline writes into the pipe before the next
  command starts, which holds 32 KB.
- A background command (`&`) that is not a plain command (a group, a
  function, a builtin, `@{...}`) runs in a new mash given this one's
  variables, functions and loaded modules (`mash -c` with a preamble).
  Inferno's runs a thread that shares them, and its make rules.
- `<{...}` and `>{...}` name a pipe as `/fd/n`, and every command
  started for that simple command gets fd n. Inferno serves the pipe as
  a file in `/chan`.
- Commands are `.wasm` files: `cmd`, then `/bin/cmd`.
- `load` knows the modules `builtins`, `make` and `history`; there are
  no loadable mash modules, and no `tk` module.
- `load history` serves `/chan/history` and `/chan/mash` from two helper
  progs, `mash --history` and `mash --serve`, which mash stops when it
  exits; a mash waiting for a command cannot answer requests itself.
  The console loop reads what is written to `/chan/mash` along with the
  console. mash does not serve `/dev/cons` in place of the console, as
  Inferno's does once history is loaded.

## fs

Each verb is a goroutine, and values move between them on Go channels
with the replies Inferno's fs uses (next, down, skip, quit). All the
verbs of Inferno's fs are here: `and bundle chstat compose depth entries
eval exec filter ls match merge mergewrite mode not or path pipe print
proto query run select setroot size unbundle void walk write`, with the
same types, usage messages, automatic conversions and error reports.

What differs:

- A shell command `@{...}` runs in a new sh each time, given `$file`
  and the other variables (`-p`, `-P`) as assignments before the block.
  Inferno's runs it in a shell context of its own, which keeps the
  variables a run sets for the next. A new sh loads the modules
  `$autoload` names, as Inferno's context does. fs does not parse the
  block itself; sh reports its syntax errors when it runs.
- `run` gets `$s` back from that sh on fd 5 (fds 3 and 4 are WASI's).
- `walk` and `write` use full path names where Inferno's change
  directory in a name space of their own.
