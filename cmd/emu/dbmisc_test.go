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

func TestAr(t *testing.T) {
	dmRun(t, `load std
d=/tmp/artest
rm -rf $d
mkdir -p $d/sub
cd $d
echo -n abc > a.o
echo -n hello > b.o
echo -n xy > sub/c.o
touch -t 1000 a.o
touch -t 200000 b.o
touch -t 3000 sub/c.o
ar rv lib.a a.o b.o
echo $status
cat lib.a
ar t lib.a
ar tv lib.a
ar q lib.a sub/c.o
ar t lib.a
ar p lib.a b.o
echo
ar pv lib.a c.o a.o
echo
ar ma a.o lib.a c.o
ar t lib.a
ar m lib.a a.o
ar t lib.a
echo -n z > d.o
ar rbv b.o lib.a d.o
ar t lib.a
ar d lib.a b.o
ar t lib.a
echo -n new > a.o
touch -t 5000 a.o
ar ru lib.a a.o
ar p lib.a a.o
echo
mkdir x
cd x
ar xo ../lib.a a.o
cat a.o
echo
ls -l a.o | sed 's/.*Jan/Jan/'
cd ..
echo errors
ar d lib.a nosuch
echo $status
ar t nolib.a
echo $status
ar t a.o
ar k lib.a
ar
ar rc new.a
ar t new.a
cd /
rm -rf $d
`, `ar: creating lib.a
a - a.o
a - b.o

!<arch>
a.o             1000        0     0     644     3         `+"`"+`
abc
b.o             200000      0     0     644     5         `+"`"+`
hello
a.o
b.o
rw-r--r--   0/0       3 Jan 01 00:16 1970  a.o
rw-r--r--   0/0       5 Jan 03 07:33 1970  b.o
a.o
b.o
c.o
hello

<a.o>

abc
<c.o>

xy
a.o
c.o
b.o
c.o
b.o
a.o
a - d.o
c.o
d.o
b.o
a.o
c.o
d.o
a.o
new
new
Jan 01  1970 a.o
errors
ar: nosuch not found
file not found
ar: cannot open nolib.a: file does not exist
error
ar: a.o not in archive format
ar: bad option `+"`"+`k'
usage: ar [uvnbailo][mrxtdpq] archive files ...
usage: ar [uvnbailo][mrxtdpq] archive files ...
`)
}

// Test9export serves a tree with 9export under listen and imports it
// both ways: Inferno's import (impo line, -p keyring -A) and the Rust
// sibling's import -9 (9P at once).
func Test9export(t *testing.T) {
	dmRun(t, `load std
d=/tmp/9xtest
rm -rf $d
mkdir -p $d/tree/sub $d/n $d/m
echo hello > $d/tree/sub/f
listen -A 'tcp!127.0.0.1!0' 9export -A -a >[2] $d/log
listen -A 'tcp!127.0.0.1!0' 9export -A -a -r >[2] /dev/null
P=tcp!`+"`"+`{cat /net/tcp/0/local}
R=tcp!`+"`"+`{cat /net/tcp/1/local}
import -c -p keyring -A $P $d/tree $d/n
ls $d/n
cat $d/n/sub/f
echo new > $d/n/g
cat $d/tree/g
unmount $d/n
import -A -9 $d/tree $P $d/m
cat $d/m/sub/f $d/m/g
echo again > $d/m/g
cat $d/tree/g
unmount $d/m
import -A -9 $d/tree $R $d/m
cat $d/m/sub/f
sh -c 'echo x > $d/m/g'
unmount $d/m
echo errors
import -A -9 $d/nosuch $P $d/m
import -p keyring -A $P $d/nosuch $d/m >[2=1] | sed 's/127.0.0.1![0-9]*/X/'
9export
echo $status
cat $d/log
rm -rf $d
`, `/tmp/9xtest/n/sub
hello
new
hello
new
again
hello
sh: cannot open /tmp/9xtest/m/g: file system read only
errors
import: bad remote tree: chdir(18:"/tmp/9xtest/nosuch"): file does not exist
import: import tcp!X /tmp/9xtest/nosuch: bad remote tree: chdir(18:"/tmp/9xtest/nosuch"): file does not exist
9export: can't open /mnt/factotum/rpc: file does not exist
factotum
9export: bad tree: /tmp/9xtest/nosuch
9export: bad tree: /tmp/9xtest/nosuch
`)
}

func TestCron(t *testing.T) {
	// 277620 is Sunday 4 January 1970, 05:07 GMT.
	dmRun(t, `d=/tmp/crontest
rm -rf $d
mkdir -p $d
echo '# a comment
7 5 * * * echo every day at 5:07	# and a comment
*/5 * * * * echo every five minutes
0-10/7 * * * * echo seven
* * 4 * 1 echo the 4th or a Monday
* * 5 * 0 echo the 5th or a Sunday
* * 5 * 1 echo the 5th or a Monday
* * * 2 * echo February
1,2,3 5 * * * echo early
7 5 * 1 0-6 echo Sunday in January
61 * * * * echo bad
* * * *
* * * * *
7/0 * * * * echo zero step' > $d/cron
cron -f $d/cron -t 277620
cron -f $d/nosuch -t 0
echo $status
cron -t
echo $status
rm -rf $d
`, `every day at 5:07
seven
the 4th or a Monday
the 5th or a Sunday
Sunday in January
cron: /tmp/crontest/cron:11: bad field '61'
cron: /tmp/crontest/cron:12: too few fields
cron: /tmp/crontest/cron:13: no command
cron: /tmp/crontest/cron:14: bad field '7/0'
cron: can't open /tmp/crontest/nosuch: file does not exist
can't open /tmp/crontest/nosuch: file does not exist
usage: cron [-f crontab] [-t time]
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
while {! ~ `+"`"+`{cat n/0} held} {sleep 0}
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
