# Simple commands, sequences, comments and quoting.
echo one; echo two	# a comment
echo 'single quoted' 'it''s' '' x
echo a#not a comment start? b
echo semi;echo colon
# Lists have no structure and may span lines.
echo (a b) ((c) d) (
	e
	f
)
((echo) (hi there) everybody)
# Concatenation is pairwise or distributive.
echo a^b (x y)^(1 2) pre^(a b c) (a b c)^post
echo -^(x y z)^.b
# Free carets.
x=stem
echo -$x.b $x^'.c' $x'.d' 'q'$x
echo $x^$x
# Blocks as arguments are converted back to text.
echo {echo  hello;   echo world}
echo {a | b && c || d} {x = 1 2; y := 3} {f >[2=1] <> g >> h <{i}}
echo {a & b; c} {$x $#x $"x ${x} `{y} "{z}}
echo {echo 'needs quote' '' 'a=b'}
# A braced block as a command runs it with $* set.
{echo hello world}
{echo $*} hello world
{{echo $*}} hello world
'{echo quoted block}'
x := {echo hello world}; $x
{$*} {echo hello world}
"{echo {echo nested}}
'{echo hello' ^ ' world}'
{}
echo empty block status $status
