cat /nonexistent
echo $status
nosuch
echo $status
/bin/nosuch.wasm
echo $status
echo ok
echo [$status]
x=1
echo [$status]
sh -c 'cat /nonexistent'
echo $status
sh -c 'nosuch'
echo $status
sh -c 'exit'
echo [$status]
sh -c 'echo ('
echo $status
sh /nonexistent
echo $status
sh -c 'load nosuchmodule; echo not here'
echo $status
load std
status foo
echo $status
! status foo
echo [$status]
! status ''
echo $status
{status inblock}
echo $status
