# Assignment and lists.
x=a; echo $x $#x
x=(a b c); echo $x $#x $"x
x=(); echo $x $#x [$"x]
(a b c) = one two three four five
echo $a / $b / $c
(p q r) = 1
echo $#p $#q $#r
y = z = both; echo $y $z
v=value; n=v; echo $$n
# $n is the nth element of $*.
{echo $1 $2 $3 $#* / $*} a b
# Local scopes.
s=global
{s:=inner; echo $s; {echo $s; s=changed; echo $s}; echo $s}
echo $s
{t:=only-inside}
echo $#t
{s=outer-changed}
echo $s
# Quoting a list.
q=(a 'b c' '' 'it''s' {echo x})
echo ${quote $q}
echo ${bquote $q}
echo $#q ${unquote ${quote $q}}
r=${unquote 'one ''two three'' four'}
echo $#r $r(2)
echo ${quote}
# Variables are exported to /env.
e=(1 'two three')
cat /env/e; echo
