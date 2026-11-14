x=`{echo a b c}
echo $#x $x
x="{echo a b c}
echo $#x $x
x=`{echo '  spaced   out  '}
echo $#x
ifs=:
x=`{echo a:b c:d}
echo $#x
ifs=()
x=`{echo 'one
two'}
echo $#x $x
# Backquotes run builtins and blocks too.
load std
x=`{for i in 1 2 3 {echo $i}}
echo $#x $x
x=`{{echo $*} with args}
echo $x
x=`echo
echo $#x
# A failed command's status is not this shell's.
x=`{cat /nonexistent >[2] /dev/null}
echo $#x [$status]
# Nested.
echo `{echo `{echo nested}}
