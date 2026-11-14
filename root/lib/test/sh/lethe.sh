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
echo 'dial tcp!127.0.0.1!1
noauth
proto hybrid' > '#λ/clone'
echo 'dial tcp!127.0.0.1!1
user glenda
key 0101010101010101010101010101010101010101010101010101010101010101
proto hybrid' > '#λ/clone'
echo 'dial tcp!127.0.0.1!1
user glenda
key 0101010101010101010101010101010101010101010101010101010101010101
proto fancy' > '#λ/clone'
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
lethe tcp!127.0.0.1!1 /mnt/shtest/l >[2] /dev/null || echo dial failed
lethe -A -k /nonexistent tcp!127.0.0.1!1 /mnt/shtest/l
lethe -A -u glenda tcp!127.0.0.1!1 /mnt/shtest/l
lethe -A -p classical tcp!127.0.0.1!1 /mnt/shtest/l
lethe -p fancy tcp!127.0.0.1!1 /mnt/shtest/l
lethe -k /lib/test/auth.key tcp!127.0.0.1!1 /mnt/shtest/l
lethe -A tcp!127.0.0.1!1 /mnt/shtest/l >[2] /dev/null || echo dial failed
rm -r /mnt/shtest/l
