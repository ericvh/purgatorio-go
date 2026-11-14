# Examples from sh(1), sh-std(1), sh-expr(1) and sh-string(1).
echo hello world
{echo hello world}
'{echo hello world}'
{echo $*} hello world
sh -c {echo hello world}
{$*} {echo hello world}
{$*} {{$*} {echo hello world}}
"{echo {echo hello world}}
'{echo hello' ^ ' world}'
x := {echo hello world}; $x
{{echo $*}} hello world
echo hi there everybody
((echo) (hi there) everybody)
echo (hi
there
everybody
)
(a b c) = one two three four five
echo $a; echo $b; echo $c
flags=O; stem=prog
echo limbo -$flags $stem.b
echo limbo -^$flags $stem^.b
x=/tmp; echo $x^/*.b
load std
filename=x.b
and (
	{~ $filename '*.b'}
	{echo file is Limbo source}
)
filename=x.c
and (
	{~ $filename '*.b'}
	{echo file is Limbo source}
)
echo ${join ' ' a b c} $"x
load expr
echo ${expr 2 1 -}
load string
echo ${slice 1 end abc} ${slice 1 ${len abc} abc}
