# mash: the scripts in /lib/test/mash, then standard input, flags,
# statuses, background commands and the history module.
mash /lib/test/mash/lang.mash >[2=1]
mash /lib/test/mash/ctl.mash >[2=1]
mash /lib/test/mash/make.mash >[2=1]
mash /lib/test/mash/misc.mash a 'b c' >[2=1]
echo '# standard input'
echo 'x = 1;
echo $x;
echo (;
echo after' | mash >[2=1]
echo 'echo no terminator needed at the end' | mash
echo '# -c, -x, -d, -n'
mash -c 'echo a; echo $x; echo b' >[2=1]
mash -c 'echo $x' >[2=1]
mash -x -c 'y = 2; echo $y; echo $y | cat'
mash -d -c 'echo a | cat; x = 1 & if (x) { echo y } else echo z'
mash -d -n -c 'case $x { a => echo a; b :: c => echo bc & }; fn f { g ^ (hd $x) }'
mash -n -c 'echo not run'
mash -cecho -c >[2=1]
echo $status
mash -q >[2=1]
echo $status
mash /nonexistent >[2=1]
echo $status
echo '# statuses'
mash -c 'echo one; cat /nonexistent; echo goes on' >[2=1]
echo $status
mash -e -c 'echo one; cat /nonexistent; echo not reached' >[2=1]
echo $status
mash -e -c 'echo $nope; echo not reached' >[2=1]
echo $status
mash -e -c 'echo (' >[2=1]
echo $status
mash -c 'test/abi usage; echo goes on' >[2=1] | sed 's/^[0-9]+ /PID /'
mash -e -c 'test/abi ''fail:x'' | echo piped; echo reached' >[2=1]
echo $status
mash -e -c 'cat /dev/null | test/abi ''fail:y''; echo not reached' >[2=1]
echo $status
mash -e -c 'run -e /nonexistent; echo not reached' >[2=1]
echo $status
mash -c 'run -e /nonexistent; echo reached' >[2=1]
echo $status
mash -c 'load make; make nosuch; echo reached' >[2=1]
echo $status
mash -e -c 'load make; make nosuch; echo not reached' >[2=1]
echo $status
mash -c 'exit; echo not reached'
mash -c 'fn f { exit }; f; echo not reached'
mash -c 'eval exit; @{ exit }; echo reached'
echo '# background'
rm -f /mnt/shtest/bg*
touch /mnt/shtest/bg1 /mnt/shtest/bg2 /mnt/shtest/bg3 /mnt/shtest/bg4 /mnt/shtest/bg5
mash -c 'echo bg > /mnt/shtest/bg1 &
	while (! "{cat /mnt/shtest/bg1}) sleep 0;
	cat /mnt/shtest/bg1'
mash -c 'v = global; fn f { echo f $v $args > /mnt/shtest/bg2 };
	f a b &
	while (! "{cat /mnt/shtest/bg2}) sleep 0;
	cat /mnt/shtest/bg2'
mash -c '{ echo grouped; echo bg; echo done > /mnt/shtest/bg4 } > /mnt/shtest/bg3 &
	while (! "{cat /mnt/shtest/bg4}) sleep 0;
	cat /mnt/shtest/bg3'
mash -c 'v = outer; @{ x := inner; echo $x $v } > /mnt/shtest/bg5 &
	while (! "{cat /mnt/shtest/bg5}) sleep 0;
	cat /mnt/shtest/bg5'
echo '# modules'
mash -c 'load make; whatis make depends; load make; load tk' >[2=1]
mash -c 'load nosuch; echo still here' >[2=1]
mash -c 'load make; load history; x = 1; fn f { echo $x }; whatis load f x'
@{
	load std
	pctl forkns
	mkdir -p /mnt/shtest/chan
	bind -c /mnt/shtest/chan /chan
	mash -c 'load history; echo one >> /chan/history; echo two >> /chan/history; cat /chan/history; cat /chan/mash' >[2=1]
}
