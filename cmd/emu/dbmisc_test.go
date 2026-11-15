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
