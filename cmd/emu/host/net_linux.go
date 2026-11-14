package host

// nosigpipe: on Linux writes use MSG_NOSIGNAL semantics through Go, which
// ignores SIGPIPE for non-stdio fds.
func nosigpipe(fd int) {}
