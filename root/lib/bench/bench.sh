# The benchmark suite of docs/perf.md, which tools/bench runs:
#	sh bench.sh where round ...
# where is host on the emu, whose /tmp is a host directory, where the
# script writes the 100 MiB file, or kernel, whose /tmp is in the RAM
# tree and already holds it (tools/bench packs it into the root).
load std
bench=/bin/test/bench.wasm
where=$1
rounds=${tl $*}
if {~ $where host} {zeros 1024 102400 > /tmp/bench.big}
mount -A {export /tmp} /n/remote
memfs /mnt
for r in $rounds {
	$bench run cat-root cat /tmp/bench.big
	$bench run cat-styx cat /n/remote/bench.big
	$bench run sh-builtin-10k sh -c 'load std; for a in 0 1 2 3 4 5 6 7 8 9 {for b in 0 1 2 3 4 5 6 7 8 9 {for c in 0 1 2 3 4 5 6 7 8 9 {for d in 0 1 2 3 4 5 6 7 8 9 {x=$d}}}}'
	$bench spawn spawn-1k 1000
	$bench fs fs-memfs-1k /mnt/d 1000
	if {~ $where host} {$bench fs fs-root-1k /tmp/benchfs 1000}
}
unmount /mnt
unmount /n/remote
if {~ $where host} {rm -rf /tmp/bench.big /tmp/benchfs}
