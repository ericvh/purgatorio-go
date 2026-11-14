package host

import "golang.org/x/sys/unix"

// nosigpipe sets SO_NOSIGPIPE, macOS's way to get EPIPE instead of SIGPIPE.
func nosigpipe(fd int) { _ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_NOSIGPIPE, 1) }

// icmpIPHeader: macOS gives an ICMP socket the message behind its IP header.
const icmpIPHeader = true
