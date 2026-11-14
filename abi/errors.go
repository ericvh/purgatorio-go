package abi

// Inferno error strings. Guests and tests compare these exactly, so they
// must match Inferno's wording.
const (
	Enonexist  = "file does not exist"
	Eperm      = "permission denied"
	Eexist     = "file already exists"
	Enotdir    = "not a directory"
	Eisdir     = "file is a directory"
	Ebadfd     = "fd out of range or not open"
	Ebadarg    = "bad arg in system call"
	Ebadaddr   = "bad address"
	Eintr      = "interrupted"
	Ehungup    = "i/o on hungup channel"
	Enotempty  = "directory not empty"
	Eio        = "i/o error"
	Enomem     = "out of memory"
	Enosys     = "not implemented"
	Ebadusefd  = "inappropriate use of fd"
	Eshortstat = "stat buffer too small"
)

// WASI preview 1 errno values used by the host.
const (
	ErrnoSuccess  = 0
	ErrnoAcces    = 2
	ErrnoBadf     = 8
	ErrnoExist    = 20
	ErrnoFault    = 21
	ErrnoIntr     = 27
	ErrnoInval    = 28
	ErrnoIO       = 29
	ErrnoIsdir    = 31
	ErrnoNoent    = 44
	ErrnoNomem    = 48
	ErrnoNospc    = 51
	ErrnoNosys    = 52
	ErrnoNotdir   = 54
	ErrnoNotempty = 55
	ErrnoNotsup   = 58
	ErrnoPipe     = 64
	ErrnoSpipe    = 70
)

var errnos = map[string]uint16{
	Enonexist: ErrnoNoent,
	Eperm:     ErrnoAcces,
	Eexist:    ErrnoExist,
	Enotdir:   ErrnoNotdir,
	Eisdir:    ErrnoIsdir,
	Ebadfd:    ErrnoBadf,
	Ebadarg:   ErrnoInval,
	Ebadaddr:  ErrnoFault,
	Eintr:     ErrnoIntr,
	Ehungup:   ErrnoPipe,
	Enotempty: ErrnoNotempty,
	Enomem:    ErrnoNomem,
	Enosys:    ErrnoNosys,
}

// Errno maps an Inferno error string to a WASI errno. Unknown strings, such
// as those from a remote Styx server, map to EIO; the caller keeps the
// string in the prog's errstr.
func Errno(errstr string) uint16 {
	if e, ok := errnos[errstr]; ok {
		return e
	}
	return ErrnoIO
}
