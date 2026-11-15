# fs: trees, entries, gates and selectors, and the verbs that run
# shell commands. chstat fixes owners, modes and times so that what is
# printed does not depend on the host.
# Shell commands run in new shells, which load what $autoload names.
autoload=std
load std
d=/mnt/shtest/fs
rm -rf $d
mkdir -p $d/t/a/b $d/t/c $d/t/d
cd $d
echo hello > t/a/x.txt
echo world > t/a/b/y.dis
echo z > t/c/z.b
fn norm {
	fs $* {chstat -u u -g g -t 0 {chstat -p {mode +d} -m 'a=rx' {chstat -p {mode -d} -m 'a=r' t}}}
}
echo '# walk, print, size'
fs print t
fs walk t
fs size t
fs entries t
echo '# gates'
fs print {select *.b t}
fs print {select {mode -d} t}
fs print {select {mode +d} t}
fs print {filter {not {match *.dis}} t}
fs print {filter -d {match *.txt} t}
fs print {filter {depth 1} t}
fs print {select {and {mode -d} {or *.b *.txt}} t}
fs print {select {match -a 't/a/*'} t}
fs print {select {match -r '^[xy]\.'} t}
fs print {filter {path t/a/b} t}
fs print {filter {path -x t/a t/d} t}
echo '# bundle, unbundle, write'
norm bundle
norm bundle > t.bundle
fs print {unbundle t.bundle}
fs print {select *.b {unbundle t.bundle}}
fs bundle t | fs write out {unbundle -}
fs print out
cat out/a/x.txt
fs write out2 {filter {depth 1} t}
fs print out2
echo '# setroot, chstat, ls'
fs print {setroot /x/y {filter {depth 1} t}}
fs print {setroot -c /x/y {filter {depth 1} t}}
fs ls {select *.dis {chstat -m 'a=rw' -u bob -g bob t}} | sed 's/ [A-Z][a-z][a-z] [0-9 :]+ / DATE /; s/ . [0-9]+ / TYPE DEV /'
fs ls -m {select *.dis {chstat -m 'a=x' -u u -g g t}} | sed 's/ [A-Z][a-z][a-z] [0-9 :]+ / DATE /; s/^\[.*\] /[muid] /; s/ . [0-9]+ / TYPE DEV /'
echo '# merge, compose, mergewrite'
mkdir -p m1/d m2/d
echo 1 > m1/d/one
echo 2 > m2/d/two
echo a > m1/same
echo b > m2/same
fs print {merge m1 m2}
fs bundle {merge m1 m2} | grep '^[ab]$'
fs bundle {merge -c {compose BoverA} m1 m2} | grep '^[ab]$'
fs print {merge -1 m1 m2}
fs print {merge -c {compose AinB} m1 m2}
fs print {merge -c {compose -d AoutB} m1 m2}
fs print {merge -c {compose AxorB} m1 m2}
fs print {merge m1 m2 t}
mkdir -p mw/a
echo old > mw/gone
echo stale > mw/a/x.txt
fs mergewrite {compose A} mw t
fs print mw
cat mw/a/x.txt
echo '# proto'
echo 'a
	x.txt
	b
		+
c
	*
nosuch' > proto
fs print {proto -r /mnt/shtest/fs/t proto}
echo '# exec, pipe, query, run'
fs exec @{echo file $file} {select *.b t}
fs exec -n 2 -t @{echo done $#file} @{echo files $file} {select {mode -d} t}
fs exec -p @{echo $file $mode $length $uid} {select *.txt {chstat -m 'a=rw' -u u t}}
fs exec -P @{echo $name $gid $#dtype $#dev $#qid} {select *.txt {chstat -g g t}}
fs pipe @{echo $file; cat} {filter {or {mode +d} *.txt} t}
fs pipe -1 @{wc -c} t
fs print {select {query @{~ $file *x*}} t}
fs print {select {match {run @{s=*.b}}} t}
fs run @{s=(a b)}
fs run @{raise oops}
echo '# errors'
fs walk /nonexistent
echo $status
fs walk t/a/x.txt
echo $status
fs nosuch x
echo $status
fs print {walk}
echo $status
fs print {match -q x}
fs print {match -r '(' t}
fs print {depth 0}
fs print {mode q t}
fs print {compose nosuch}
fs print {select x t t}
fs print t '{'
fs eval '{print t}'
fs print {select {query @nocmd} t}
fs print $1
fs
echo $status
