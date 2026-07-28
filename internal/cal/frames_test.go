package cal

import (
	"errors"
	"testing"
)

// The frame tests are written against the FIRMWARE parsers, not against
// MMDVMCal, because the firmware is what accepts or NAKs these bytes:
// MMDVM_HS's SerialPort.cpp::setConfig for the protocol-1 form and
// g4klx/MMDVM's for protocol 2. Each assertion below names the field the parser
// reads at that offset, so a future reader can check the claim against the
// parser rather than against this file.

func TestFrameV1MatchesTheHotspotParser(t *testing.T) {
	c := SweepConfig(3, 40)
	c.TXDelay = 10
	c.DMRDelay = 7
	c.Levels.RX = 60
	c.Levels.TXDCOff = -6
	c.Levels.RXDCOff = 9

	b, err := c.FrameV1()
	if err != nil {
		t.Fatalf("FrameV1: %v", err)
	}
	if len(b) != 27 {
		t.Fatalf("frame is %d bytes, want 27 (the length MMDVMCal and MMDVM-Host both send)", len(b))
	}
	if b[0] != 0xE0 || b[1] != 27 || b[2] != CmdSetConfig {
		t.Fatalf("header = % X, want E0 1B 02", b[:3])
	}

	d := b[3:]
	for _, tc := range []struct {
		field string
		at    int
		want  byte
	}{
		{"flags: simplex only", 0, 0x80},
		{"mode enables: DMR", 1, 0x02},
		{"TX delay", 2, 10},
		{"state", 3, byte(StateDMR)},
		{"RX level (60% → 153)", 4, 153},
		{"CW-ID level tracks TX level (40% → 102)", 5, 102},
		{"colour code", 6, 3},
		{"DMR delay", 7, 7},
		{"the dead OscOffset byte", 8, 128},
		{"D-Star TX level", 9, 102},
		{"DMR TX level", 10, 102},
		{"YSF TX level", 11, 102},
		{"P25 TX level", 12, 102},
		{"TX DC offset (-6 → 122)", 13, 122},
		{"RX DC offset (+9 → 137)", 14, 137},
		{"NXDN TX level", 15, 102},
		{"POCSAG TX level", 17, 102},
		{"M17 TX level", 21, 102},
	} {
		if d[tc.at] != tc.want {
			t.Errorf("payload[%d] (%s) = %d, want %d", tc.at, tc.field, d[tc.at], tc.want)
		}
	}
}

func TestFrameV2MatchesTheRepeaterParser(t *testing.T) {
	c := SweepConfig(11, 40)
	c.POCSAG = true
	c.RXInvert = true
	c.PTTInvert = true
	c.Levels.RX = 60
	c.Levels.TXDCOff = 4

	b, err := c.FrameV2()
	if err != nil {
		t.Fatalf("FrameV2: %v", err)
	}
	if len(b) != 40 {
		t.Fatalf("frame is %d bytes, want 40 — 37 of payload, which is exactly the minimum g4klx/MMDVM accepts", len(b))
	}

	d := b[3:]
	for _, tc := range []struct {
		field string
		at    int
		want  byte
	}{
		{"flags: RX invert | PTT invert | simplex", 0, 0x01 | 0x04 | 0x80},
		{"mode enables byte 1: DMR", 1, 0x02},
		{"mode enables byte 2: POCSAG moved here from bit 5", 2, 0x01},
		{"state", 4, byte(StateDMR)},
		{"TX DC offset (+4 → 132)", 5, 132},
		{"RX DC offset (0 → 128)", 6, 128},
		{"RX level (60% → 153)", 7, 153},
		{"CW-ID level", 8, 102},
		{"D-Star TX level", 9, 102},
		{"FM TX level", 16, 102},
		{"colour code", 26, 11},
	} {
		if d[tc.at] != tc.want {
			t.Errorf("payload[%d] (%s) = %d, want %d", tc.at, tc.field, d[tc.at], tc.want)
		}
	}
}

// TestPOCSAGDoesNotBecomeFM is the confusable difference between the two forms
// written down as a test: bit 5 of the mode byte is POCSAG in protocol 1 and FM
// in protocol 2. Getting it wrong enables a mode the operator did not ask for on
// a board that may not have it.
func TestPOCSAGDoesNotBecomeFM(t *testing.T) {
	c := SweepConfig(1, 50)
	c.POCSAG = true

	v1, err := c.FrameV1()
	if err != nil {
		t.Fatal(err)
	}
	if got := v1[4] & 0x20; got == 0 {
		t.Error("protocol 1: POCSAG should be bit 5 of the single mode byte")
	}

	v2, err := c.FrameV2()
	if err != nil {
		t.Fatal(err)
	}
	if got := v2[4] & 0x20; got != 0 {
		t.Error("protocol 2: bit 5 of the first mode byte is FM, and POCSAG must not set it")
	}
	if v2[5]&0x01 == 0 {
		t.Error("protocol 2: POCSAG belongs in the second mode byte")
	}
}

func TestValidateRefusesWhatTheFirmwareWouldNAK(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"a state whose mode is not enabled", Config{State: StateDMR, Simplex: true}},
		{"a TX delay past the firmware's limit", Config{State: StateIdle, TXDelay: 51}},
		{"a colour code outside 0-15", Config{State: StateIdle, ColorCode: 16}},
		{"a DC offset outside a signed byte", Config{State: StateIdle, Levels: Levels{TXDCOff: 200}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.FrameV1(); !errors.Is(err, ErrConfig) {
				t.Fatalf("FrameV1 error = %v, want ErrConfig", err)
			}
			if _, err := tc.cfg.FrameV2(); !errors.Is(err, ErrConfig) {
				t.Fatalf("FrameV2 error = %v, want ErrConfig", err)
			}
		})
	}
}

// TestSweepConfigCannotTransmit is the safety property of RFC-0021 §2 stated as
// a test. The sweep runs in an ordinary receive state, and the firmware will not
// key from one.
func TestSweepConfigCannotTransmit(t *testing.T) {
	c := SweepConfig(1, 50)
	if c.State.Transmits() {
		t.Fatalf("the sweep runs in %s, which this package classes as a transmitting state", c.State)
	}
	if !c.Simplex {
		t.Error("the sweep must be simplex; a duplex config is NAKed with reason 6 on a single-radio board")
	}
	if !c.DMR {
		t.Error("StateDMR without the DMR enable is NAKed with reason 4")
	}
}

func TestTXConfigRefusesAReceiveState(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("TXConfig(StateDMR) returned instead of panicking")
		}
	}()
	TXConfig(StateDMR, 1, 50)
}

func TestTXConfigEnablesTheModeItsStateNeeds(t *testing.T) {
	for _, tc := range []struct {
		state State
		check func(Config) bool
	}{
		{StateDMRCal, func(c Config) bool { return c.DMR }},
		{StateDMRDMO1K, func(c Config) bool { return c.DMR }},
		{StateDStarCal, func(c Config) bool { return c.DStar }},
		{StatePOCSAGCal, func(c Config) bool { return c.POCSAG }},
		{StateFMCal12K, func(c Config) bool { return c.FM }},
	} {
		c := TXConfig(tc.state, 1, 50)
		if !tc.check(c) {
			t.Errorf("%s did not enable the mode its firmware state maps onto", tc.state)
		}
		if _, err := c.FrameV1(); err != nil {
			t.Errorf("%s: %v", tc.state, err)
		}
	}
}

func TestSetFreqIsLittleEndian(t *testing.T) {
	b := SetFreq(438800000, 438800500, 100)
	if len(b) != 13 {
		t.Fatalf("SET_FREQ is %d bytes, want 13", len(b))
	}
	if b[2] != CmdSetFreq {
		t.Fatalf("command byte = 0x%02X", b[2])
	}
	rx := uint32(b[4]) | uint32(b[5])<<8 | uint32(b[6])<<16 | uint32(b[7])<<24
	tx := uint32(b[8]) | uint32(b[9])<<8 | uint32(b[10])<<16 | uint32(b[11])<<24
	if rx != 438800000 || tx != 438800500 {
		t.Fatalf("decoded rx=%d tx=%d", rx, tx)
	}
	if b[12] != 255 {
		t.Fatalf("100%% power = %d, want 255", b[12])
	}
}

func TestSetTransmitFrames(t *testing.T) {
	on := setTransmit(true)
	off := setTransmit(false)
	if len(on) != 4 || on[2] != CmdCalData || on[3] != 0x01 {
		t.Fatalf("key frame = % X", on)
	}
	if off[3] != 0x00 {
		t.Fatalf("unkey frame = % X", off)
	}
}

func TestParseLevels(t *testing.T) {
	// inverted, max = 0x0BB8 (3000), min = 0xF448 (-3000)
	r, err := ParseLevels([]byte{0x80, 0x0B, 0xB8, 0xF4, 0x48})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Inverted {
		t.Error("the inverted flag was not read")
	}
	if r.Max != 3000 || r.Min != -3000 || r.Diff != 6000 || r.Centre != 0 {
		t.Fatalf("got %+v", r)
	}
	if _, err := ParseLevels([]byte{0x00, 0x01}); err == nil {
		t.Error("a short level reply was accepted")
	}
}

func TestParseRSSI(t *testing.T) {
	r, err := ParseRSSI([]byte{0x01, 0x00, 0x00, 0xC8, 0x00, 0xFA})
	if err != nil {
		t.Fatal(err)
	}
	if r.Max != 256 || r.Min != 200 || r.Ave != 250 {
		t.Fatalf("got %+v", r)
	}
}

func TestNAKErrorExplainsTheReasonCode(t *testing.T) {
	err := &NAKError{Command: CmdSetConfig, Reason: 6}
	if got := err.Error(); got == "" || !contains(got, "duplex") {
		t.Fatalf("reason 6 should mention the duplex-on-a-single-radio case, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
