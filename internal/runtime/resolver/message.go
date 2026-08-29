package resolver

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
)

const (
	headerLen  = 12
	typeA      = 1
	typeAAAA   = 28
	classINET  = 1
	defaultTTL = 30

	flagResponse      = 0x8000
	flagAuthoritative = 0x0400
	flagRecursionOK   = 0x0100
	rcodeNameError    = 3
)

var errMalformed = errors.New("malformed dns message")

type question struct {
	name  string
	qtype uint16
	end   int
}

func parseQuestion(msg []byte) (question, error) {
	if len(msg) < headerLen {
		return question{}, errMalformed
	}
	if binary.BigEndian.Uint16(msg[4:6]) != 1 {
		return question{}, errMalformed
	}

	var labels []string
	offset := headerLen

	for {
		if offset >= len(msg) {
			return question{}, errMalformed
		}
		length := int(msg[offset])
		offset++

		if length == 0 {
			break
		}
		if length&0xC0 != 0 {
			return question{}, errMalformed
		}
		if offset+length > len(msg) {
			return question{}, errMalformed
		}
		labels = append(labels, string(msg[offset:offset+length]))
		offset += length
	}

	if offset+4 > len(msg) {
		return question{}, errMalformed
	}

	return question{
		name:  strings.ToLower(strings.Join(labels, ".")),
		qtype: binary.BigEndian.Uint16(msg[offset : offset+2]),
		end:   offset + 4,
	}, nil
}

func answer(query []byte, q question, addr netip.Addr) []byte {
	reply := make([]byte, 0, q.end+16)
	reply = append(reply, query[:q.end]...)

	var flags uint16 = flagResponse | flagAuthoritative
	if binary.BigEndian.Uint16(query[2:4])&flagRecursionOK != 0 {
		flags |= flagRecursionOK
	}
	binary.BigEndian.PutUint16(reply[2:4], flags)
	binary.BigEndian.PutUint16(reply[6:8], 1)
	binary.BigEndian.PutUint16(reply[8:10], 0)
	binary.BigEndian.PutUint16(reply[10:12], 0)

	kind, size := uint16(typeA), uint16(4)
	if addr.Is6() {
		kind, size = typeAAAA, 16
	}
	raw := addr.AsSlice()

	reply = binary.BigEndian.AppendUint16(reply, 0xC000|headerLen)
	reply = binary.BigEndian.AppendUint16(reply, kind)
	reply = binary.BigEndian.AppendUint16(reply, classINET)
	reply = binary.BigEndian.AppendUint32(reply, defaultTTL)
	reply = binary.BigEndian.AppendUint16(reply, size)

	return append(reply, raw...)
}

func emptyAnswer(query []byte, q question, rcode uint16) []byte {
	reply := make([]byte, 0, q.end)
	reply = append(reply, query[:q.end]...)

	var flags uint16 = flagResponse | flagAuthoritative
	flags |= rcode
	if binary.BigEndian.Uint16(query[2:4])&flagRecursionOK != 0 {
		flags |= flagRecursionOK
	}
	binary.BigEndian.PutUint16(reply[2:4], flags)
	binary.BigEndian.PutUint16(reply[6:8], 0)
	binary.BigEndian.PutUint16(reply[8:10], 0)
	binary.BigEndian.PutUint16(reply[10:12], 0)

	return reply
}
