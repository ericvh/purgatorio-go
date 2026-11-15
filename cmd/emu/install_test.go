package main

import "testing"

// The package tools (Inferno's appl/cmd/install), in the scratch tree of
// disk_test.go: create a package and an update, install them, and look
// at what is installed.
func TestInstall(t *testing.T) {
	diskCheck(t, `cd /tmp/m5disk
install/create -t 1000000 -d 'test package' -p proto -s src demo
echo status:$status
install/info 1000000
install/inst -v -r /tmp/m5disk/inst 1000000
echo status:$status
du -a inst | sed 's/^[0-9]+	//'
cat inst/wrap/demo/1000000/md5sum inst/wrap/demo/1000000/desc
install/info -r /tmp/m5disk/inst demo
install/inst -h -r /tmp/m5disk/inst 1000000 /dis/lib
echo changed > inst/dis/a.dis
install/wdiff -l -r /tmp/m5disk/inst 1000000
install/wdiff -r /tmp/m5disk/inst 1000000
echo three > src/dis/lib/b
touch -t 2000 src/dis/lib/b
install/create -u -t 1000100 -r /tmp/m5disk/inst -s src demo
disk/mkext -h < 1000100
install/inst -r /tmp/m5disk/inst 1000100
cat inst/dis/a.dis inst/dis/lib/b
install/info -r /tmp/m5disk/inst demo
install/inst -r /tmp/m5disk/inst 1000000
echo status:$status
install/create -x -p proto -s src demo
mkdir -p inst/install/demo
cp 1000000 1000100 inst/install/demo
bind -a /tmp/m5disk/inst /
install/wrap2list -i
install/wfind a.dis lib/b
install/wfind -p demo nosuch
install/proto2list absproto | sed 's/	[0-9]+	-?[0-9]+$//'
install/ckproto -r /tmp/m5disk/src proto
install/ckproto -o -m -r /tmp/m5disk/src allproto
install/ckproto -v -r /tmp/m5disk/src proto
install/mkproto src
install/mkproto -README src/dis src/README
install/info nosuch
install/inst
echo status:$status
install/create -t 5 demo
`, `archiving package demo to 1000000
wrap pass proto
processing proto
file system made
status:
demo (complete as of Mon Jan 12 13:46:40 GMT 1970)
package 1000000: test package
installing directory /tmp/m5disk/inst/wrap
installing directory /tmp/m5disk/inst/wrap/demo
installing directory /tmp/m5disk/inst/wrap/demo/1000000
installing directory /tmp/m5disk/inst/dis
installing directory /tmp/m5disk/inst/dis/lib
done
status:
1    inst/dis/a.dis
1    inst/dis/lib/b
1    inst/dis/lib
1    inst/dis
1    inst/wrap/demo/1000000/desc
1    inst/wrap/demo/1000000/md5sum
0    inst/wrap/demo/1000000/package
1    inst/wrap/demo/1000000/proto
1    inst/wrap/demo/1000000
1    inst/wrap/demo
1    inst/wrap
1    inst
/dis 00000000000000000000000000000000
/dis/a.dis 5bbf5a52328e7439ae6e719dfe712200
/dis/lib 00000000000000000000000000000000
/dis/lib/b c193497a1a06b2c72230e6146ff47080
test package
demo (complete as of Mon Jan 12 13:46:40 GMT 1970)
package 1000000: test package
/tmp/m5disk/inst/dis/lib 20000000775 inferno inferno 981201600 0
/tmp/m5disk/inst/dis/lib/b 664 inferno inferno 981201600 4
done
/dis/a.dis modified
1c1
< one
---
> changed
archiving package demo to 1000100
wrap pass /tmp/m5disk/inst/wrap/demo/1000000/proto
processing /tmp/m5disk/inst/wrap/demo/1000000/proto
file system made
done
/wrap 20000000775 inferno inferno 1000100 0
/wrap/demo 20000000775 inferno inferno 1000100 0
/wrap/demo/1000100 20000000775 inferno inferno 1000100 0
/wrap/demo/1000100/update 664 inferno inferno 1000100 23
/wrap/demo/1000100/package 664 inferno inferno 1000100 0
/wrap/demo/1000100/desc 664 inferno inferno 1000100 8
/wrap/demo/1000100/proto 664 inferno inferno 1000100 20
/wrap/demo/1000100/md5sum 664 inferno inferno 1000100 44
/wrap/demo/1000100/remove 664 inferno inferno 1000100 0
/dis/lib/b 664 inferno inferno 2000 6
done
changed
three
demo (complete as of Mon Jan 12 13:48:20 GMT 1970)
full update 1000100 updating 1000000: inferno
package 1000000: test package
skipping /dis/a.dis: locally modified
skipping /dis/lib/b: version from newer package exists
done
status:
processing proto
/dis	981201600	0
/dis/a.dis	981201600	4
/dis/lib	981201600	0
/dis/lib/b	2000	6
end of archive
/dis	-	0	-2147483139	0	0	0
/dis/a.dis	-	8	436	0	1864167084	0
/dis/lib	-	0	-2147483139	0	0	0
/dis/lib/b	-	6	436	0	-12139048	0
/dis/lib/b
	c193497a1a06b2c72230e6146ff47080 demo/1000000(package )	/install/demo
	febe6995bad457991331348f7b9c85fa demo/1000100(full upd)	/install/demo
	c193497a1a06b2c72230e6146ff47080 demo/1000000(package )	/wrap/demo
	febe6995bad457991331348f7b9c85fa demo/1000100(full upd)	/wrap/demo
	febe6995bad457991331348f7b9c85fa /dis/lib/b
/dis/a.dis
	5bbf5a52328e7439ae6e719dfe712200 demo/1000000(package )	/install/demo
	5bbf5a52328e7439ae6e719dfe712200 demo/1000000(package )	/wrap/demo
	ec1bebaea2c042beb68f7679ddd106a4 /dis/a.dis
/tmp/m5disk/src	-	0	-2147483155	981201600
/tmp/m5disk/src/dis	-	0	-2147483155	981201600
/tmp/m5disk/src/dis/a.dis	-	4	420	981201600	-132667233	0
/tmp/m5disk/src/dis/lib	-	0	-2147483155	981201600
/tmp/m5disk/src/README missing
/tmp/m5disk/src/adm missing
/tmp/m5disk/src/dis/lib/c missing
/tmp/m5disk/src/dis/lib/zeros missing
file /tmp/m5disk/src/README not 664 at least
/tmp/m5disk/src/adm missing
directory /tmp/m5disk/src/dis not 775 at least
directory /tmp/m5disk/src/dis/lib not 775 at least
file /tmp/m5disk/src/dis/lib/b not 664 at least
file /tmp/m5disk/src/dis/lib/c not 664 at least
file /tmp/m5disk/src/dis/lib/zeros not 664 at least
/dis /tmp/m5disk/src/dis
/dis/a.dis /tmp/m5disk/src/dis/a.dis
/dis/lib /tmp/m5disk/src/dis/lib
/dis/lib/b /tmp/m5disk/src/dis/lib/b
dis
	a.dis
	lib
		b
tmp
	m5disk
		src
			dis
				a.dis
				lib
					b
/tmp/m5disk/src/README missing
/tmp/m5disk/src/adm missing
/tmp/m5disk/src/dis/lib/c missing
/tmp/m5disk/src/dis/lib/zeros missing
README
adm
	users
dis
	a.dis
	lib
		b
		c
		zeros
src/dis
	a.dis
	lib
		b
		c
		zeros
src/README
no such package found
Usage: inst [-h] [-u] [-v] [-f] [-c] [-F] [-r dest-root] [file ...]
status:usage
archiving package demo to 5
fatal: proto file missing
`)
}

// updatelog, applylog and mergelog keep a replica in step with a tree.
func TestInstallLog(t *testing.T) {
	diskCheck(t, `cd /tmp/m5disk
install/updatelog -u u -g g -p lproto -r srv -t 2000 0 empty.log > s.log
cat s.log
> c.log
install/applylog -v c.log cli srv < s.log
echo status:$status
cat c.log
cat cli/f cli/d/g
echo changed > srv/f
touch -t 3000 srv/f
rm srv/d/g
install/updatelog -u u -g g -p lproto -r srv -t 4000 0 s.log > s2.log
cat s2.log
install/updatelog -c -u u -g g -p lproto -r srv -t 4000 0 s.log
echo 0 0 > stamp
install/applylog -v -T stamp c.log cli srv < s2.log
cat stamp
echo
cat cli/f
ls cli/d
install/mergelog s.log < s2.log
cat s.log s2.log > all.log
echo local > cli/f
echo again > srv/f
touch -t 5000 srv/f
install/updatelog -u u -g g -p lproto -r srv -t 6000 0 all.log > s3.log
cat s3.log
install/applylog -T stamp c.log cli srv < s3.log
echo status:$status
install/applylog -n c.log cli srv < s3.log
install/applylog -s c.log cli srv < s3.log
cat cli/f
tail -2 c.log
echo 'bad line' | install/applylog c.log cli srv
echo status:$status
install/applylog c.log cli nosuch
echo status:$status
install/mergelog
echo status:$status
install/updatelog -p lproto -r nosuch empty.log
echo status:$status
`, `2000 0 a f - 644 u g 981201600 4
2000 1 a d - 20000000755 u g 981201600 0
2000 2 a d/g - 644 u g 981201600 4
a f 644 u g 981201600
a d 20000000755 u g 981201600
a d/g 644 u g 981201600
maxseq: 2000 2
status:
2000 0 a f - 644 u g 981201600 4
2000 1 a d - 20000000755 u g 981201600 0
2000 2 a d/g - 644 u g 981201600 4
one
two
updatelog: warning[old=srv/d/g]: can't stat file srv/d/g: file does not exist
4000 0 c f - 644 u g 3000 8
4000 1 d d/g - 644 u g 981201600 4
updatelog: warning[old=srv/d/g]: can't stat file srv/d/g: file does not exist
4000 0 c f - 644 u g 3000 8
c f
d d/g
maxseq: 4000 1
       4000           1
changed
2000 1 a d - 20000000755 u g 981201600 0
4000 0 c f - 644 u g 3000 8
4000 1 d d/g - 644 u g 981201600 4
updatelog: warning[old=srv/d/g]: can't stat file srv/d/g: file does not exist
6000 0 c f - 644 u g 5000 6
f: locally modified; will not update
maxseq: 4000 1
status:1 conflicts
c f
f: locally modified; will not update
maxseq: 0 0
maxseq: 6000 0
again
4000 1 d d/g - 644 u g 981201600 4
6000 0 c f - 644 u g 5000 6
applylog: bad log entry: 'bad line'
status:|error
applylog: can't stat server root nosuch: file does not exist
status:error
usage: mergelog [-vd] oldlog <newlog
status:usage
updatelog: can't stat replica root nosuch: file does not exist
status:error
`)
}
