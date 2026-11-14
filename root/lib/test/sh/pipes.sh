echo one | cat
echo two | cat | cat | cat
{echo a; echo b; echo c} | cat | {cat; echo end} | cat
x=val
{echo $x} | cat
cat /nonexistent |[2] cat
echo a |
	cat
# Statuses of the commands of a pipeline.
cat /nonexistent | cat
echo $status
cat /nonexistent | cat /nonexistent | cat
echo $status
nosuch | cat
echo $status
echo ok | cat
echo [$status]
