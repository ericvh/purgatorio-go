package main

import (
	"testing"
)

// The tests of dbm, dbfs, rawdbfs, ar, 9export, aescbc, ai2key, cron and
// tar work in scratch directories under /tmp that their scripts make and
// remove, so both wazero modes start from the same state.

// dmRun runs script with sh in the emu and checks its output and status.
func dmRun(t *testing.T, script, want string) {
	t.Helper()
	out, st := emu(t, "", "sh", "-c", script)
	if out != want || st != "" {
		t.Errorf("got status %q output:\n%s\nwant:\n%s", st, out, want)
	}
}

func TestDbm(t *testing.T) {
	dmRun(t, `load std
d=/tmp/dbmtest
rm -rf $d
mkdir -p $d
cd $d
cat /dev/null > db.dir
cat /dev/null > db.pag
dbm/store db one 1
echo 'two	2 with a tab before it
three three, after a space
four 4' | dbm/store db
dbm/fetch db one
echo
dbm/fetch db three
echo
dbm/keys db | sort
dbm/list db | sort
echo errors
dbm/store db one again
echo $status
dbm/fetch db nosuch
dbm/delete db two
dbm/delete db two
dbm/keys db | sort
echo 'five 5
noseparator
six 6' | dbm/store db
echo $status
dbm/fetch db six
echo many
ls -p /bin | sed 's/$/ a value long enough to fill pages soon/' | dbm/store db
ls -p /bin | grep '\.wasm$' | sort > want
dbm/keys db | grep '\.wasm$' | sort > got
cmp want got && echo same keys
dbm/fetch db sh.wasm
echo
dbm/fetch nodb one
dbm/fetch
echo $status
cd /
rm -rf $d
`, `1
three, after a space
four
one
three
two
four	4
one	1
three	three, after a space
two	2 with a tab before it
errors
one exists
store
not found
not found
four
one
three
dbm/store: bad input
|error
not found
many
same keys
a value long enough to fill pages soon
dbm/fetch: nodb: file does not exist
usage: dbm/fetch dbfile key
usage
`)
}

func TestDbfs(t *testing.T) {
	dmRun(t, `load std
d=/tmp/dbfstest
rm -rf $d
mkdir -p $d/n
cd $d
echo 'one
two

three
' > db
dbfs db n
ls n
cat n/1
echo 'first
record' > n/new
echo second > n/new
ls n
cat n/2
echo changed > n/0
rm n/1
ls n
cat db
echo 'bad

record' > n/3
unmount n
echo reloaded
dbfs db n
ls n
cat n/1
unmount n
echo errors
dbfs nodb n
echo $status
dbfs -e newdb n
ls n
cat newdb
unmount n
dbfs
echo $status
cd /
rm -rf $d
`, `n/0
n/1
n/new
three
n/0
n/1
n/2
n/3
n/new
first
record
n/0
n/2
n/3
n/new
changed

first
record

second

echo: write error: partial or malformed record
reloaded
n/0
n/1
n/2
n/new
first
record
errors
dbfs: can't open nodb: file does not exist
open
n/new
Usage: dbfs [-a|-b|-ac|-bc] [-D] file mountpoint
usage
`)
}

func TestRawdbfs(t *testing.T) {
	dmRun(t, `load std
d=/tmp/rawdbfstest
rm -rf $d
mkdir -p $d/n
cd $d
rawdbfs -e db n
ls n
echo 'first

record, with an empty line' > n/new
echo second > n/new
dd -if /bin/rawdbfs.wasm -bs 4096 -count 1 -of bin >[2] /dev/null
cp bin n/new
ls n
cat n/0
cmp bin n/2 && echo same binary
echo changed > n/1
rm n/0
ls n
unmount n
echo reloaded
rawdbfs db n
ls n
cat n/0
cmp bin n/2 && echo same binary
rm n/2
echo a record longer than the twenty bytes it had > n/0
cat n/0
unmount n
echo -x
rawdbfs -x -u 'echo changed' db n
ls n
cat n/stats
echo
cat n/index
echo 41 > n/index
cat n/index
echo
cat n/index
echo
echo more > n/new
ls n
cat n/0 n/2
cat n/stats
echo
unmount n
echo -l
rawdbfs -l db n
{echo held; sleep 2} > n/0 &
while {! ~ ` + "`" + `{cat n/0} held} {sleep 0}
sh -c 'echo other > n/0'
while {! sh -c 'echo again > n/0' >[2] /dev/null} {sleep 0}
cat n/0
unmount n
echo errors
rawdbfs nodb n
echo this is not a data base > junk
rawdbfs junk n
rawdbfs
cd /
rm -rf $d
`, `n/new
n/0
n/1
n/2
n/new
first

record, with an empty line
same binary
n/1
n/2
n/new
reloaded
n/0
n/2
n/new
changed
same binary
a record longer than the twenty bytes it had
-x
n/0
n/index
n/new
n/stats
0 0 0 0
cat: error reading n/index: permission denied
42
43
changed
n/0
n/2
n/index
n/new
n/stats
a record longer than the twenty bytes it had
more
5 3 1 0
-l
sh: cannot open n/0: open/create -- file is locked
again
errors
dbfs: can't open nodb: file does not exist
dbfs: can't read junk: found bad header 'this is no'

Usage: dbfs [-abcelrxD][-u cmd] file mountpoint
`)
}
