package host

// nosigpipe: on Linux writes use MSG_NOSIGNAL semantics through Go, which
// ignores SIGPIPE for non-stdio fds.
func nosigpipe(fd int) {}

// icmpIPHeader: Linux gives an ICMP socket the message alone, and the ttl as a control message.
const icmpIPHeader = false
