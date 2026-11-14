# Memory and prog limits (docs/abi.md): memlimit, #pdomain, domain limits.
load std
me=${pid}
(held limit) = `{cat /prog/$me/mem}
echo limit $limit
{echo domain proglimit 2 > /prog/$me/ctl}
{echo memlimit 4q > /prog/$me/ctl}
{echo memlimit > /prog/$me/ctl}
echo memlimit 256m > /prog/$me/ctl
(held limit) = `{cat /prog/$me/mem}
echo limit $limit
{echo memlimit 1g > /prog/$me/ctl}
sh -c 'load std; me=${pid}; (held limit) = `{cat /prog/$me/mem}; echo child limit $limit'
sh -c 'load std; me=${pid}; echo memlimit 1m > /prog/$me/ctl; echo not reached' >[2] /dev/null
echo status $status
{ls '#pnonsense'}
@{
	pctl newpgrp forkns
	sub=${pid}
	bind '#pdomain' /prog
	ls /prog > /mnt/shtest/progs
	wc -l < /mnt/shtest/progs
	test -d /prog/$me || echo outer prog hidden
	test -d '#p'/$me && echo outer prog in '#p'
	ls '#pdomain='^$sub > /mnt/shtest/progs
	wc -l < /mnt/shtest/progs
	sh -c 'load std; pctl nodevs; ls ''#pdomain'''
	sh -c 'load std; pctl nodevs; ls ''#p'''
	sh -c 'load std; pctl nodevs; ls ''#pdomain=1'''
	echo domain proglimit 2 > /prog/$sub/ctl
	{echo domain proglimit 3 > /prog/$sub/ctl}
	echo one
	{sh -c 'echo two'} >[2] /dev/null
	echo status $status
	echo domain memlimit 1k > /prog/$sub/ctl
	{echo three} >[2] /dev/null
}
echo outside $status
