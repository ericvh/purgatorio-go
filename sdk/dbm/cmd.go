package dbm

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// OpenCmd is Open for the dbm commands: when the data base cannot be
// opened it prints "prog: file: error" and exits with "fail:open".
func OpenCmd(prog, file string, mode int) *Dbf {
	db, err := Open(file, mode)
	if err != nil {
		os.Stderr.WriteString(prog + ": " + file + ": " + sys.Errstr() + "\n")
		sys.Exits("fail:open")
	}
	return db
}

// Must returns v, or when err is set prints it and exits with it as the
// status, as the exception Inferno's dbm raises ends the command.
func Must[T any](v T, err error) T {
	if err != nil {
		os.Stdout.Sync()
		os.Stderr.WriteString(err.Error() + "\n")
		sys.Exits(err.Error())
	}
	return v
}
