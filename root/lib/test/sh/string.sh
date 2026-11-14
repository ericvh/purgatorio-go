load std
load string
prefix ab abc && echo prefix yes
prefix abc ab || echo prefix no $status
in b abc && echo in yes
in z a-y || echo in no
in z '^a-y' && echo in negated
echo ${splitl 'hello world' ' '} / ${splitr a/b/c /} / ${splitl abc z}
echo ${splitstrl 'one::two::three' ::} / ${splitstrr 'one::two::three' ::}
echo ${drop '   x  ' ' '}^. / ${take aabbcc ab}
echo ${tolower 'MiXeD Case'} ${toupper 'MiXeD Case'}
echo ${len héllo} ${alen héllo} ${len ''}
echo ${slice 1 3 abcdef} ${slice 2 end abcdef} ${slice 4 100 abc}^.
echo ${fields : a::b:c} / $#x
x=${fields : a::b:c}; echo $#x
echo '['^${padl 6 ab}^']' '['^${padr 6 ab}^']' '['^${padl 1 abc}^']' '['^${padl 6 a b}^']'
# Usage errors raise exceptions.
sh -c 'load string; echo ${len}'
sh -c 'load string; echo ${slice x 1 abc}'
sh -c 'load string; prefix a'
sh -c 'load string; echo ${padl x}'
