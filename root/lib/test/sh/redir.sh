d=/mnt/shtest
echo first > $d/r1
echo second >> $d/r1
cat $d/r1
cat < $d/r1
echo replaced > $d/r1; cat $d/r1
# <> opens for reading and writing, creating the file.
echo -n > $d/r2
cat <> $d/r2
echo rw >[1] $d/r2; cat $d/r2
# fd forms.
cat /nonexistent >[2] $d/r3; echo status $status
cat $d/r3
cat /nonexistent >[2=1] > $d/r4
cat $d/r4
{echo to stderr >[1=2]} >[2] $d/r5
cat $d/r5
# Redirections are applied left to right.
cat /nonexistent >$d/r6 >[2=1]
echo r6:; cat $d/r6
# A redirected block runs in this shell.
{x=set-in-block; echo out} > $d/r7
cat $d/r7; echo $x
# Pipes as files.
cat <{echo from a pipe}
cat <{echo one} <{echo two}
# Errors raise an exception, which ends a script.
sh -c 'cat < /nonexistent; echo not reached'
echo $status
sh -c 'echo x > /nonexistent/dir/file'
echo $status
sh -c 'echo a >[1=9]'
echo $status
sh -c 'echo (a b) > (x y)'
echo $status
sh -c 'echo a >[1=]'
echo $status
sh -c 'echo a >[x]'
echo $status
echo done
