package netstack

import (
	"github.com/ericvh/purgatorio-go/platform"
)

// ProtoStats gives gVisor's TCP, UDP, ICMP and ICMPv6 counters under
// Inferno's names (platform.ProtoStatser). Counters gVisor does not keep
// (HlenErrs, LenErrs and OutOfOrder, and of the ICMP error counts all
// but InErrors) are 0. ICMP messages sent through /net/icmp's raw
// endpoints are counted here, as gVisor does not.
func (s *Stack) ProtoStats(kind int) ([]uint64, bool) {
	st := s.g.Stack.Stats()
	switch kind {
	case platform.TCP:
		t := st.TCP
		return []uint64{0, t.ActiveConnectionOpenings.Value(), t.PassiveConnectionOpenings.Value(),
			t.EstablishedResets.Value(), t.CurrentEstablished.Value(),
			t.ValidSegmentsReceived.Value() + t.InvalidSegmentsReceived.Value(), t.SegmentsSent.Value(),
			t.Retransmits.Value(), t.Timeouts.Value(), t.InvalidSegmentsReceived.Value(), t.ResetsSent.Value(),
			t.ChecksumErrors.Value(), 0, 0, 0}, true
	case platform.UDP:
		u := st.UDP
		return []uint64{u.PacketsReceived.Value(), u.UnknownPortErrors.Value(),
			u.MalformedPacketsReceived.Value() + u.ReceiveBufferErrors.Value() + u.ChecksumErrors.Value(),
			u.PacketsSent.Value()}, true
	case platform.ICMP:
		in, out := st.ICMP.V4.PacketsReceived, st.ICMP.V4.PacketsSent
		const n, nt = 6, 19
		v := make([]uint64, n+2*nt)
		pair := func(t int, i, o uint64) {
			v[n+2*t] += i
			v[n+2*t+1] += o
		}
		pair(0, in.EchoReply.Value(), out.EchoReply.Value())
		pair(3, in.DstUnreachable.Value(), out.DstUnreachable.Value())
		pair(4, in.SrcQuench.Value(), out.SrcQuench.Value())
		pair(5, in.Redirect.Value(), out.Redirect.Value())
		pair(8, in.EchoRequest.Value(), out.EchoRequest.Value())
		pair(11, in.TimeExceeded.Value(), out.TimeExceeded.Value())
		pair(12, in.ParamProblem.Value(), out.ParamProblem.Value())
		pair(13, in.Timestamp.Value(), out.Timestamp.Value())
		pair(14, in.TimestampReply.Value(), out.TimestampReply.Value())
		pair(15, in.InfoRequest.Value(), out.InfoRequest.Value())
		pair(16, in.InfoReply.Value(), out.InfoReply.Value())
		s.icmptotals(v, n, nt, 0, in.Invalid.Value())
		return v, true
	case platform.ICMPv6:
		in, out := st.ICMP.V6.PacketsReceived, st.ICMP.V6.PacketsSent
		const n, nt = 12, 138
		v := make([]uint64, n+2*nt)
		pair := func(t int, i, o uint64) {
			v[n+2*t] += i
			v[n+2*t+1] += o
		}
		pair(1, in.DstUnreachable.Value(), out.DstUnreachable.Value())
		pair(2, in.PacketTooBig.Value(), out.PacketTooBig.Value())
		pair(3, in.TimeExceeded.Value(), out.TimeExceeded.Value())
		pair(4, in.ParamProblem.Value(), out.ParamProblem.Value())
		pair(128, in.EchoRequest.Value(), out.EchoRequest.Value())
		pair(129, in.EchoReply.Value(), out.EchoReply.Value())
		pair(133, in.RouterSolicit.Value(), out.RouterSolicit.Value())
		pair(134, in.RouterAdvert.Value(), out.RouterAdvert.Value())
		pair(135, in.NeighborSolicit.Value(), out.NeighborSolicit.Value())
		pair(136, in.NeighborAdvert.Value(), out.NeighborAdvert.Value())
		pair(137, in.RedirectMsg.Value(), out.RedirectMsg.Value())
		s.icmptotals(v, n, nt, 1, in.Invalid.Value())
		return v, true
	}
	return nil, false
}

// icmptotals adds the raw endpoints' messages sent to v's per-type
// counts and fills in InMsgs, InErrors and OutMsgs.
func (s *Stack) icmptotals(v []uint64, n, nt, fam int, invalid uint64) {
	var in, out uint64
	for t := range nt {
		v[n+2*t+1] += s.icmp.out[fam][t].Load()
		in += v[n+2*t]
		out += v[n+2*t+1]
	}
	v[0], v[1], v[2] = in+invalid, invalid, out
}
