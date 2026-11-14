package abi

// Inferno error strings, with Inferno's names and wording (emu/port/error.c).
// Guests and tests compare these exactly. The second group are strings
// Inferno uses elsewhere (file servers, the shell) that the core needs too.
const (
	Enoerror     = "no error"
	Emount       = "inconsistent mount"
	Eunmount     = "not mounted"
	Eunion       = "not in union"
	Emountrpc    = "mount rpc error"
	Eshutdown    = "mounted device shut down"
	Eowner       = "not owner"
	Eunknown     = "unknown user or group id"
	Enocreate    = "mounted directory forbids creation"
	Enonexist    = "file does not exist"
	Eexist       = "file already exists"
	Ebadsharp    = "unknown device in # filename"
	Enotdir      = "not a directory"
	Eisdir       = "file is a directory"
	Ebadchar     = "bad character in file name"
	Efilename    = "file name syntax"
	Eperm        = "permission denied"
	Ebadusefd    = "inappropriate use of fd"
	Ebadarg      = "bad arg in system call"
	Einuse       = "device or object already in use"
	Eio          = "i/o error"
	Etoobig      = "read or write too large"
	Etoosmall    = "read or write too small"
	Enetaddr     = "bad network address"
	Emsgsize     = "message is too big for protocol"
	Enetbusy     = "network device is busy or allocated"
	Enoproto     = "network protocol not supported"
	Enoport      = "network port not available"
	Enoifc       = "bad interface or no free interface slots"
	Enolisten    = "not announced"
	Ehungup      = "i/o on hungup channel"
	Ebadctl      = "bad process or channel control request"
	Enodev       = "no free devices"
	Enoenv       = "no free environment resources"
	Emuxshutdown = "mux server shut down"
	Emuxbusy     = "all mux channels busy"
	Emuxmsg      = "bad mux message format or mismatch"
	Ethread      = "thread exited"
	Enochild     = "no living children"
	Eioload      = "i/o error in demand load"
	Enovmem      = "out of memory: virtual memory"
	Ebadld       = "illegal line discipline"
	Ebadfd       = "fd out of range or not open"
	Eisstream    = "seek on a stream"
	Ebadexec     = "exec header invalid"
	Etimedout    = "connection timed out"
	Econrefused  = "connection refused"
	Econinuse    = "connection in use"
	Enetunreach  = "network unreachable"
	Eintr        = "interrupted"
	Enomem       = "out of memory: kernel"
	Esfnotcached = "subfont not cached"
	Esoverlap    = "segments overlap"
	Emouseset    = "mouse type already set"
	Eshort       = "i/o count too small"
	Enobitstore  = "out of screen memory"
	Egreg        = "jim'll fix it"
	Ebadspec     = "bad attach specifier"
	Estopped     = "thread must be stopped"
	Enoattach    = "mount/attach disallowed"
	Eshortstat   = "stat buffer too small"
	Enegoff      = "negative i/o offset"
	Ebadstat     = "malformed stat buffer"
	Ecmdargs     = "wrong #args in control message"
	Enofd        = "no free file descriptors"
	Enoctl       = "unknown control request"

	Enospace  = "file system full"
	Enotimpl  = "not implemented"
	Enotsock  = "not a socket"
	Enotempty = "directory not empty"
	Exdev     = "rename across directories"
	Enoproc   = "no free procs"
	Eismount  = "inconsistent mount"
)

// Older names used in this tree.
const (
	Enotseek = Eisstream
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
	ErrnoNotsock  = 57
	ErrnoNotsup   = 58
	ErrnoPipe     = 64
	ErrnoSpipe    = 70
	ErrnoXdev     = 75
)

// Ebadaddr is reported for a guest pointer outside its memory. Inferno
// has no such case; the wording follows Plan 9.
const Ebadaddr = "bad address"

var errnos = map[string]uint16{
	Enonexist: ErrnoNoent,
	Eperm:     ErrnoAcces,
	Eowner:    ErrnoAcces,
	Eexist:    ErrnoExist,
	Enotdir:   ErrnoNotdir,
	Eisdir:    ErrnoIsdir,
	Ebadfd:    ErrnoBadf,
	Ebadusefd: ErrnoBadf,
	Ebadarg:   ErrnoInval,
	Efilename: ErrnoInval,
	Ebadchar:  ErrnoInval,
	Enegoff:   ErrnoInval,
	Ebadaddr:  ErrnoFault,
	Eintr:     ErrnoIntr,
	Ehungup:   ErrnoPipe,
	Enotempty: ErrnoNotempty,
	Enospace:  ErrnoNospc,
	Enomem:    ErrnoNomem,
	Enotimpl:  ErrnoNosys,
	Enotsock:  ErrnoNotsock,
	Eisstream: ErrnoSpipe,
	Exdev:     ErrnoXdev,
	Enocreate: ErrnoAcces,
	Enoattach: ErrnoAcces,
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
