package transport

import (
	"encoding/binary"
	"errors"
)

const maxReplyPayloadBytes = 1 << 20

var errReplyFrame = errors.New("invalid runner reply frame")

func buildReplyFrame(magic string, correlation, payload []byte) ([]byte, error) {
	if len(magic) != 8 || len(correlation) != 32 || len(payload) == 0 || len(payload) > maxReplyPayloadBytes {
		return nil, errReplyFrame
	}
	reply := append([]byte(nil), magic...)
	reply = append(reply, correlation...)
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, uint32(len(payload)))
	reply = append(reply, size...)
	reply = append(reply, payload...)
	return reply, nil
}
