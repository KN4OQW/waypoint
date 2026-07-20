package peer

import "testing"

// FuzzDecode drives arbitrary bytes through the message decoder: it must never
// panic, and any accepted message must re-encode to something the decoder accepts
// (self-consistency). A hostile peer's bytes either decode or error — never crash
// (matching the frames-parser fuzz contract, Prompt 3).
func FuzzDecode(f *testing.F) {
	// Seed corpus: every valid message type, plus known truncations/garbage.
	for _, m := range allSamples() {
		f.Add(m.Encode()[2:]) // the body (post length-prefix), which Decode consumes
	}
	f.Add([]byte{})
	f.Add([]byte{ProtocolVersion})
	f.Add([]byte{ProtocolVersion, byte(MsgVoice)})
	f.Add([]byte{ProtocolVersion, byte(MsgHello), 3, 'a', 'b', 'c', 0})
	f.Add([]byte{ProtocolVersion, byte(MsgVoice), 255})
	f.Add([]byte{9, 9, 9, 9})

	f.Fuzz(func(t *testing.T, body []byte) {
		m, err := Decode(body)
		if err != nil {
			return
		}
		// A decoded message must re-encode and re-decode to the same value.
		again, err := Decode(m.Encode()[2:])
		if err != nil {
			t.Fatalf("re-decode of an accepted message failed: %v (msg %+v)", err, m)
		}
		if again.Type != m.Type {
			t.Fatalf("re-decode changed type: %s -> %s", m.Type, again.Type)
		}
	})
}
