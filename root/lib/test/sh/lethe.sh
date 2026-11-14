# The Lethe client, #λ, with no server (docs/lethe.md): its clone file,
# settings it refuses (noauth, the clear mode, takes no key and no user),
# and the lethe command's errors. Reading a tree needs lethed on the
# host: cmd/emu/lethe_test.go and the kernel's -net check.
load std
mkdir /mnt/shtest/l
ls '#λ'
ls -d '#λ/clone'
echo nonsense > '#λ/clone'
echo 'dial udp!127.0.0.1!1' > '#λ/clone'
read < '#λ/clone'
echo 'dial tcp!127.0.0.1!1
noauth
key 00' > '#λ/clone'
echo 'dial tcp!127.0.0.1!1
noauth yes' > '#λ/clone'
# Nothing listens on port 1: the write fails (connection refused on the
# emu, network unavailable on a kernel without one).
{echo 'dial tcp!127.0.0.1!1
noauth' > '#λ/clone'} >[2] /dev/null || echo dial failed
echo 'dial tcp!127.0.0.1!1
noauth
path /a/../b' > '#λ/clone'
ls '#λ99'
lethe
lethe -k /nonexistent tcp!127.0.0.1!1 /mnt/shtest/l
lethe tcp!127.0.0.1!1 /mnt/shtest/l
lethe -A -k /nonexistent tcp!127.0.0.1!1 /mnt/shtest/l
lethe -A -u glenda tcp!127.0.0.1!1 /mnt/shtest/l
lethe -A tcp!127.0.0.1!1 /mnt/shtest/l >[2] /dev/null || echo dial failed
rm -r /mnt/shtest/l
