# mcpfs: another MCP server's tools as files, with mcpd as the far end.
# Each tool is a directory with its schema and a call file: write the
# arguments, then read the result on the same open file.
load std
d=/mnt/shtest/mcpfs
mkdir $d
mcpfs -m $d mcpd
cd $d
ls
ls tools | wc -l
ls tools/run
# mcpd speaks 2026-07-28, so server is its server/discover result.
grep -s '"supportedVersions":\["2026-07-28"' server && echo discovered
grep -s 'serverInfo":{"name":"mcpd"' server && echo server info
grep -s '"name":"run"' tools/run/schema && echo run schema
grep -s '"inputSchema"' tools/read_file/schema && echo read_file schema
{echo '{"command":"echo hello from the far end; ns | grep tmp | wc -l"}' >[1=0]; cat} <> tools/run/call
{echo '{"path":"/lib/hello.txt"}' >[1=0]; cat} <> tools/read_file/call | grep -s 'hello from the root' && echo read_file result
# No arguments are {}; a call that fails says so in isError.
cat tools/job_list/call
{echo '{"path":"/nonexistent"}' >[1=0]; cat} <> tools/stat/call
# What is not a JSON object fails the read.
{echo 'not json' >[1=0]; cat} <> tools/run/call
{echo '[1]' >[1=0]; cat} <> tools/run/call
# The far end checks the arguments.
{echo '{"nonsense":1}' >[1=0]; cat} <> tools/run/call
cat tools/nosuch/call
@{echo x > tools/run/schema}
# Resources: the list as JSON, and a resource's contents by its URI.
grep -s '"resources"' resources/list && echo resource list
{echo purg:///lib/hello.txt >[1=0]; cat} <> resources/read
{echo purg:///nonexistent >[1=0]; cat} <> resources/read
cat resources/read
@{echo x > resources/list}
# Prompts: the list as JSON, and one prompt by its parameters.
grep -s '"name":"inferno-sh"' prompts/list && echo prompt list
{echo '{"name":"inferno-sh"}' >[1=0]; cat} <> prompts/get | grep -s '"role":"user"' && echo prompt
{echo '{"name":"nosuch"}' >[1=0]; cat} <> prompts/get
# A call does not hold up the mount: while one sleeps, the tree is
# listed and read (mcpd itself runs its run calls one at a time).
{echo '{"command":"sleep 2; echo slow"}' >[1=0]; cat} <> tools/run/call > $d.slow &
sleep 0
ls tools | wc -l
grep -s '"name":"stat"' tools/stat/schema && echo stat schema meanwhile
grep -s '"stdout":"slow' $d.slow || echo slow call not yet answered
for i in 1 2 3 4 5 6 7 8 9 10 {grep -s '"stdout":"slow' $d.slow || sleep 1}
grep -s '"stdout":"slow' $d.slow && echo slow call answered
# Two calls at once: a job_read that waits for the job's output, and a
# job_write, made while it waits, that gives it some.
{echo '{"argv":["cat"]}' >[1=0]; cat} <> tools/job_start/call > /dev/null
{echo '{"id":1,"wait_ms":30000}' >[1=0]; cat} <> tools/job_read/call > $d.read &
sleep 1
grep -s hello $d.read || echo job_read waits
{echo '{"id":1,"data":"hello\n"}' >[1=0]; cat} <> tools/job_write/call
for i in 1 2 3 4 5 6 7 8 9 10 {grep -s hello $d.read || sleep 1}
grep -s '"stdout":"hello\\n"' $d.read && echo job_read answered
cd /
unmount $d
rm $d.slow $d.read
# With -t, a call not answered in time fails, and the mount goes on.
mcpfs -t 300 -m $d mcpd
{echo '{"command":"sleep 5"}' >[1=0]; cat} <> $d/tools/run/call
ls $d/tools | wc -l
unmount $d
# The same server in revision 2025-11-25 (/lib/test/mcp/legacy refuses
# server/discover): server is its initialize result.
mcpfs -m $d sh /lib/test/mcp/legacy
grep -s '"protocolVersion":"2025-11-25"' $d/server && echo initialized
{echo '{"command":"echo legacy"}' >[1=0]; cat} <> $d/tools/run/call
{echo purg:///lib/hello.txt >[1=0]; cat} <> $d/resources/read
grep -s '"name":"inferno-sh"' $d/prompts/list && echo legacy prompt list
unmount $d
# A server that says its tools changed is asked for them again: tool a
# goes and b comes (/lib/test/mcp/changing).
mcpfs -m $d sh /lib/test/mcp/changing
for i in 1 2 3 4 5 6 7 8 9 10 {test -d $d/tools/b || sleep 1}
ls $d/tools
test -d $d/tools/a || echo a gone
# The server's log message, kept in log.
cat $d/log
unmount $d
# The same in 2026-07-28: the change comes on subscriptions/listen.
mcpfs -m $d sh /lib/test/mcp/changing-modern
for i in 1 2 3 4 5 6 7 8 9 10 {test -d $d/tools/b || sleep 1}
ls $d/tools
grep -s changing-modern $d/server && echo modern server
cat $d/log
unmount $d
rm -r $d
mcpfs -m /n nosuchcommand
echo status $status
