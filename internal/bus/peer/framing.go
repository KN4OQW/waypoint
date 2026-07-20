package peer

import (
	"encoding/binary"
	"fmt"
	"io"
)

// framing.go is the length-prefixed message framing over a byte stream. It is
// still socket-free: it works over any io.Reader/io.Writer (a bytes.Buffer in
// tests, net.Pipe in the topology sim, a *tls.Conn in the field).

// WriteMessage writes one length-prefixed message. It is a single Write of the
// fully-encoded buffer so a message is never torn across the wire by this layer.
func WriteMessage(w io.Writer, m Message) error {
	_, err := w.Write(m.Encode())
	return err
}

// ReadMessage reads exactly one length-prefixed message. A length exceeding
// maxMessage is rejected before any large read, so a hostile prefix cannot force a
// big allocation or a long read.
func ReadMessage(r io.Reader) (Message, error) {
	var lenbuf [2]byte
	if _, err := io.ReadFull(r, lenbuf[:]); err != nil {
		return Message{}, err
	}
	n := int(binary.BigEndian.Uint16(lenbuf[:]))
	if n < 2 {
		return Message{}, ErrShort
	}
	if n > maxMessage {
		return Message{}, fmt.Errorf("peer: message length %d exceeds max %d", n, maxMessage)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Message{}, err
	}
	return Decode(body)
}
