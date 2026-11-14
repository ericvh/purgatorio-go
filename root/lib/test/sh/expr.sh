load std expr
echo ${expr 1 2 +} ${expr 2 1 -} ${expr 6 7 x} ${expr 6 7 '*'} ${expr 7 2 /} ${expr 7 2 %}
echo ${expr 1 2 3 4 + rep} ${expr 1 5 seq} ${expr 5 1 seq} ${expr 1 2 3}
echo ${expr 3 _} ${expr 5 3 and} ${expr 5 3 or} ${expr 5 3 xor} ${expr 0 '~'}
echo ${expr 1 4 shl} ${expr 16 2 shr} ${expr 1 2 eq} ${expr 2 2 '=='} ${expr 1 2 neq} ${expr 1 2 lt} ${expr 2 1 gt} ${expr 2 2 le} ${expr 2 2 ge}
echo ${expr 0 not} ${expr 3 '!'}
echo ${expr -r 16 255} ${expr -r2 5} ${expr 16rff 1 +} ${expr 2r101} ${expr -r 16 -255}
echo ${expr 9223372036854775807 1 +}
ntest 1 && echo ntest one
ntest 0 || echo ntest zero $status
sh -c 'load expr; echo ${expr 1 0 /}'
echo $status
sh -c 'load expr; echo ${expr +}'
sh -c 'load expr; echo ${expr 1 foo}'
sh -c 'load expr; echo ${expr -r 99 1}'
sh -c 'load expr; echo ${expr 1 rep}'
