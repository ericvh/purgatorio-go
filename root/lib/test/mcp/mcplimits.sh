# mcpd stage 5: a session is a prog domain with limits. Past -M a program
# fails and the result says it was the memory limit; a fork bomb stops at
# -P with no free procs; a second session goes on meanwhile. A prog that
# left its group, and whose parent exited, dies with the session. The
# session has no #¤, and file2chan works in it. Twenty background sleeps
# after the bomb reach the prog limit however slowly the bomb spreads.
load std expr
d=/mnt/shtest/mcplimits
rm -rf $d
mkdir $d
fn init {
	echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"sh","version":"0"}}}'
}
fn call {
	echo '{"jsonrpc":"2.0","id":'^$1^',"method":"tools/call","params":{"name":"run","arguments":{"command":"'^$2^'"}}}'
}
fn has {
	grep '"id":'^$1^',' $d/$2 | grep -s $3 && echo $1 $4
}
{
	init
	echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run","arguments":{"command":"load std; x=xxxxxxxxxxxxxxxx; while {} {x=$x^$x}","timeout_ms":300000}}}'
	call 3 'echo after the memory'
	call 4 'load std; fn bomb {bomb & bomb &}; bomb; for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 {sleep 100 &}'
	echo '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"reset","arguments":{}}}'
	call 6 'echo after the bomb'
	call 7 'load std; @{pctl newpgrp; sleep 1000 & echo stray $apid}'
} | mcpd -M 128m -P 16 > $d/a &
bombed=$apid
{
	init
	call 2 'sleep 2; echo other session'
	call 3 'ls ''#¤'''
	call 4 'load std; ramfile /chan/mine mine & while {! test -f /chan/mine} {sleep 1}; cat /chan/mine'
} | mcpd > $d/b
has 2 b 'other session' 'other session ran'
has 3 b '#¤: mount/attach disallowed' 'no factotum'
has 4 b '"stdout":"mine",' 'own srv file'
n=0
while {and {test -d /prog/$bombed} {ntest ${expr 120 $n -}}} {
	sleep 1
	n=${expr $n 1 +}
}
has 2 a 'out of memory: the session''s programs may hold 128 MiB' 'memory limit'
has 3 a 'after the memory' 'served on'
has 4 a 'no free procs: the session may run at most 16 programs' 'prog limit'
has 5 a '"reset":true' 'reset'
has 6 a 'after the bomb' 'served on'
# (A quoted " inside `{...} trips this sh, so the pattern is a variable.)
pat='"id":7,'
stray=`{grep $pat $d/a | sed 's/.*stray //' | sed 's/[^0-9].*//'}
~ $#stray 0 && echo no stray pid
n=0
while {and {test -d /prog/$stray} {ntest ${expr 60 $n -}}} {
	sleep 1
	n=${expr $n 1 +}
}
test -d /prog/$stray || echo stray gone
rm -r $d
