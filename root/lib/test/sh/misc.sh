load std
# Background commands set $apid and are not waited for.
echo $#apid
cat /dev/null &
echo $#apid [$status]
{cat /dev/null} &
~ $apid '[1-9]*' || echo bad apid
# A subshell.
@{echo in a subshell}
@ echo external in a subshell
@{status sub}
echo $status
@
# whatis.
x=(a 'b c')
whatis x echo {a; b} '{c}' @ loaded
whatis nosuch
echo $status
fn f {echo f}
whatis f if hd
builtin echo builtin runs externals
builtin f
echo $status
builtin whatis whatis
# loaded and unload.
echo ${loaded}
load string expr std string
echo ${loaded}
unload expr
echo ${loaded}
unload nosuch
echo $status
loaded >/mnt/shtest/loaded; getlines {~ $line 'fn*' && echo $line} </mnt/shtest/loaded
# run and scripts with arguments.
echo 'echo $0 / $* / $1 / $#*' > /mnt/shtest/script
run /mnt/shtest/script a b
sh /mnt/shtest/script c d e
sh -c 'echo $0 / $*' x y
{echo $0} arg
# The script's $* is restored after run.
{run /mnt/shtest/script inner; echo $*} outer
# exit ends the shell with no error.
sh -c 'echo before; exit; echo after'
echo [$status]
# -e stops at the first failing command.
sh -e -c 'load std; echo one; status two; echo three'
echo $status
echo 'load std; status bad; echo after bad' > /mnt/shtest/escript
sh -e /mnt/shtest/escript
echo $status
# -x prints commands.
sh -x -c 'echo traced; x=1; {echo in block}'
# syncenv and the environment.
v=exported
syncenv
cat /env/v; echo
{loc:=local; cat /env/loc; echo}
echo $#loc
cat /env/loc
echo done
