package auth9

import (
	"errors"
	"io"
	"strconv"
)

// ServeTicket answers ticket requests on rw until it closes, with the
// behaviour authsrv(6) gives them, from keys, which maps a user to its
// DES key (PassToKey). It is for tests; it has no other requests.
//
// The reply is AuthOK and two tickets for the request's user with a new
// key: the client's, sealed with the requesting host's key (HostID),
// and the server's, sealed with the server's key (AuthID). A host may
// ask only for itself. An unknown user, or a host asking for another,
// still gets a reply, sealed with random keys, so the reply tells a
// caller nothing it could not already open.
func ServeTicket(rw io.ReadWriter, keys map[string][]byte) error {
	msg := make([]byte, TICKREQLEN)
	for {
		if _, err := io.ReadFull(rw, msg); err != nil {
			return nil
		}
		req, _ := UnpackTicketreq(msg)
		if req.Type != AuthTreq {
			reply := make([]byte, 1+AERRLEN)
			reply[0] = AuthErr
			puts(reply[1:], "unsupported request")
			rw.Write(reply)
			return errors.New("unsupported request type " + strconv.Itoa(int(req.Type)))
		}
		if _, err := rw.Write(ticketReply(req, keys)); err != nil {
			return err
		}
	}
}

func ticketReply(req *Ticketreq, keys map[string][]byte) []byte {
	sealer := func(user string) []byte {
		if k, ok := keys[user]; ok && req.HostID == req.UID {
			return k
		}
		k := make([]byte, DESKEYLEN)
		Random(k)
		return k
	}
	tk := Ticket{Chal: req.Chal, CUID: req.UID}
	if req.HostID == req.UID {
		tk.SUID = req.UID
	}
	Random(tk.Key[:])
	reply := []byte{AuthOK}
	for _, side := range []struct {
		num  byte
		user string
	}{{AuthTc, req.HostID}, {AuthTs, req.AuthID}} {
		tk.Num = side.num
		reply = append(reply, tk.Pack(sealer(side.user))...)
	}
	return reply
}
