package core

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/platform"
)

// The counters of each protocol's stats file, by Inferno's names: tcp.c,
// udp.c, icmp.c and icmp6.c.
var (
	tcpstatnames = []string{"MaxConn", "ActiveOpens", "PassiveOpens", "EstabResets", "CurrEstab", "InSegs",
		"OutSegs", "RetransSegs", "RetransTimeouts", "InErrs", "OutRsts", "CsumErrs", "HlenErrs", "LenErrs", "OutOfOrder"}
	udpstatnames   = []string{"InDatagrams", "NoPorts", "InErrors", "OutDatagrams"}
	icmpstatnames  = []string{"InMsgs", "InErrors", "OutMsgs", "CsumErrs", "LenErrs", "HlenErrs"}
	icmp6statnames = []string{"InMsgs", "InErrors", "OutMsgs", "CsumErrs", "LenErrs", "HlenErrs", "HoplimErrs",
		"IcmpCodeErrs", "TargetErrs", "OptlenErrs", "AddrmxpErrs", "RouterAddrErrs"}

	// icmpnames names the ICMP types 0 to 18, as icmp.c (sic the
	// spaces).
	icmpnames = map[int]string{0: "EchoReply", 3: "Unreachable", 4: "SrcQuench", 5: "Redirect", 8: "EchoRequest",
		11: "TimeExceed", 12: "InParmProblem", 13: "Timestamp", 14: "TimestampReply", 15: "InfoRequest",
		16: "InfoReply", 17: "AddrMaskRequest", 18: "AddrMaskReply  "}
	// icmpnames6 names the ICMPv6 types icmp6.c counts, of 0 to 137;
	// it lists only these.
	icmpnames6 = map[int]string{0: "EchoReply", 1: "UnreachableV6", 2: "PacketTooBigV6", 3: "TimeExceedV6",
		4: "SrcQuench", 5: "Redirect", 8: "EchoRequest", 11: "TimeExceed", 12: "InParmProblem", 13: "Timestamp",
		14: "TimestampReply", 15: "InfoRequest", 16: "InfoReply", 17: "AddrMaskRequest", 18: "AddrMaskReply",
		128: "EchoRequestV6", 129: "EchoReplyV6", 133: "RouterSolicit", 134: "RouterAdvert", 135: "NbrSolicit",
		136: "NbrAdvert", 137: "RedirectV6"}
)

const (
	icmpMaxtype  = 18
	icmp6Maxtype = 137
)

// protostats formats a protocol's counters (platform.ProtoStatser) as
// its stats file.
func protostats(kind int, v []uint64) string {
	at := func(i int) uint64 {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	var b strings.Builder
	names := func(l []string) {
		for i, n := range l {
			fmt.Fprintf(&b, "%s: %d\n", n, at(i))
		}
	}
	switch kind {
	case platform.TCP:
		// MaxConn is #I's limit of conversations.
		v = append([]uint64{ipMaxconv}, v[min(1, len(v)):]...)
		names(tcpstatnames)
	case platform.UDP:
		names(udpstatnames)
	case platform.ICMP:
		names(icmpstatnames)
		o := len(icmpstatnames)
		for t := 0; t <= icmpMaxtype; t++ {
			n, ok := icmpnames[t]
			if !ok {
				n = strconv.Itoa(t)
			}
			fmt.Fprintf(&b, "%s: %d %d\n", n, at(o+2*t), at(o+2*t+1))
		}
	case platform.ICMPv6:
		names(icmp6statnames)
		o := len(icmp6statnames)
		for t := 0; t <= icmp6Maxtype; t++ {
			if n, ok := icmpnames6[t]; ok {
				fmt.Fprintf(&b, "%s: %d %d\n", n, at(o+2*t), at(o+2*t+1))
			}
		}
	}
	return b.String()
}
