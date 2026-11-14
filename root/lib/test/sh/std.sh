load std
# Conditions.
if {~ a a} {echo a is a}
if {~ a b} {echo wrong} {echo else}
if {~ a b} {echo wrong} {~ a a} {echo second} {echo else}
~ abc 'a*' && echo match star
~ abc 'a?c' && echo match query
~ abc '[a-c]bc' && echo match class
~ abc '*d' || echo no match $status
~ x
echo $status
no && echo no args
no a || echo args $status
and {echo first} {status bad} {echo not reached}
echo and $status
or {status bad} {echo second} {echo not reached}
echo or $status
! {status x} && echo negated
# Loops.
for i in a b c {echo for $i}
for i in {echo not run}
x=(a b c)
while {! no $x} {echo while ${hd $x}; x=${tl $x}}
apply {echo apply $1} 1 2 3
for i in 1 2 3 4 5 {if {~ $i 2} {raise continue}; if {~ $i 4} {raise break}; echo loop $i}
apply {if {~ $1 b} {raise break}; echo $1} a b c
y=0
while {~ $y 0} {y=1; raise continue}
echo while continued $y
{echo l1; echo l2; echo l3} | getlines {echo got $line}
echo a:b:c | getlines : {echo field $line}
# Functions.
fn greet {echo hello $*}
greet world
whatis greet
fn greet
greet again
echo $status
subfn double {result=$*^$*}
echo ${double a b}
whatis double
fn a b {echo shared $0}
a; b
# Exceptions.
rescue 'oops*' {echo rescued $exception} {echo before; raise oops; echo not reached}
rescue '*' {echo rescued $exception} {nosuchbuiltin=1; ${nosuch}}
rescue '*' {echo outer $exception} {rescue '*' {echo inner $exception; raise second} {raise first}}
echo $status
x=`{rescue '*' {echo handled} {raise inbq}}
echo $x
# Substitution builtins.
echo ${hd a b c} / ${tl a b c} / ${index 2 a b c} / ${index 5 a b}
echo ${split ab a:b::c} / ${join - a b c} / ${join -}
ifs=' '
echo ${split 'one two  three'}
echo ${parse '{echo   parsed}'}
${parse '{echo run parsed}'}
echo $#apid; ~ ${pid} '[1-9]*' && echo pid is a number
e=1; echo ${env} | getlines ' ' {~ $line e && echo env has e}
# flag and status.
flag x && echo x set
echo flag $status
flag e +; flag e && echo e set; flag e -
status
echo [$status]
