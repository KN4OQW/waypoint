package modem

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// versionFrameV1 builds the wire bytes a protocol-1 modem answers with.
func versionFrameV1(desc string) []byte {
	body := append([]byte{cmdGetVersion, 1}, []byte(desc)...)
	return append([]byte{frameStart, byte(len(body) + 2)}, body...)
}

// versionFrameV2 builds a protocol-2 reply. udid is padded/truncated to the 16
// bytes the layout reserves, which is what makes the description offset fixed.
func versionFrameV2(cap1, cap2, cpu byte, udid []byte, desc string) []byte {
	id := make([]byte, 16)
	copy(id, udid)
	body := []byte{cmdGetVersion, 2, cap1, cap2, cpu}
	body = append(body, id...)
	body = append(body, []byte(desc)...)
	return append([]byte{frameStart, byte(len(body) + 2)}, body...)
}

func TestVersionRequestIsTheOnlyFrameWeSend(t *testing.T) {
	got := VersionRequest()
	want := []byte{0xE0, 0x03, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("VersionRequest() = % X, want % X", got, want)
	}
	// A caller mutating the returned slice must not poison the next probe.
	got[2] = 0xFF
	if VersionRequest()[2] != 0x00 {
		t.Error("VersionRequest returns shared state; it must build a fresh frame")
	}
}

func TestReadFrameResyncsToTheStartByte(t *testing.T) {
	// A port that has just been opened may hold the tail of someone else's
	// traffic. The reader must find the next real frame rather than choke.
	noise := []byte{0x11, 0x22, 0x33}
	r := bytes.NewReader(append(noise, versionFrameV1("MMDVM_HS_Hat-v1.5.1")...))
	f, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Type != cmdGetVersion {
		t.Errorf("Type = %#x, want %#x", f.Type, cmdGetVersion)
	}
}

func TestReadFrameBoundsTheResyncScan(t *testing.T) {
	// A device that is not a modem can emit anything forever. "Wait for 0xE0"
	// without a bound is a hang, and this probe runs inside an HTTP handler.
	r := bytes.NewReader(bytes.Repeat([]byte{0x55}, maxSkip+10))
	if _, err := ReadFrame(r); err == nil {
		t.Fatal("ReadFrame accepted an unbounded stream with no frame start")
	}
}

func TestReadFrameRejectsImplausibleLength(t *testing.T) {
	for name, frame := range map[string][]byte{
		"too short": {frameStart, 0x02, cmdGetVersion},
		"truncated": {frameStart, 0x20, cmdGetVersion, 0x01},
	} {
		if _, err := ReadFrame(bytes.NewReader(frame)); err == nil {
			t.Errorf("%s: ReadFrame accepted a malformed frame", name)
		}
	}
}

func TestReadFrameTwoByteLengthForm(t *testing.T) {
	// Length 0 in the one-byte field means "next byte, plus 255".
	payload := bytes.Repeat([]byte{0x5A}, 300)
	total := 3 + 1 + len(payload) // header(3) + type + payload
	frame := append([]byte{frameStart, 0x00, byte(total - 255), cmdGetVersion}, payload...)
	f, err := ReadFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(f.Payload) != len(payload) {
		t.Errorf("payload len = %d, want %d", len(f.Payload), len(payload))
	}
}

func TestReadFrameSurfacesASilentPort(t *testing.T) {
	// The common real-world case: nothing is attached, or the thing attached is
	// not a modem. It must come back as an error, not a zero frame.
	if _, err := ReadFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestParseVersionProtocol1(t *testing.T) {
	desc := "MMDVM_HS_Hat-v1.5.1 20210423 12.288MHz ADF7021 FW by CA6JAU GitID #29b0d34"
	f, err := ReadFrame(bytes.NewReader(versionFrameV1(desc)))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVersion(f)
	if err != nil {
		t.Fatal(err)
	}
	if v.Protocol != 1 {
		t.Errorf("Protocol = %d, want 1", v.Protocol)
	}
	if v.Description != desc {
		t.Errorf("Description = %q, want %q", v.Description, desc)
	}
	if v.UDID != "" {
		t.Errorf("UDID = %q, want empty — protocol 1 reports none", v.UDID)
	}
}

// The bench board, byte for byte from docs/on-hardware-report.md.
func TestParseVersionBenchDualHat(t *testing.T) {
	const desc = "MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a"
	f, err := ReadFrame(bytes.NewReader(versionFrameV1(desc)))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVersion(f)
	if err != nil {
		t.Fatal(err)
	}
	d := ParseDescription(v.Description)
	if d.HWType != "MMDVM_HS_Dual_Hat" || d.Firmware != "1.6.1" || d.TCXOHz != 14_745_600 || !d.Dual {
		t.Fatalf("bench board parsed as %+v", d)
	}
	if got := Resolve(d, TransportGPIO); got.BoardID == "" && len(got.Candidates) == 0 {
		t.Fatal("bench board resolved to nothing")
	}
}

func TestParseVersionProtocol2(t *testing.T) {
	const desc = "MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU"
	udid := []byte{0xDE, 0xAD, 0xBE, 0xEF, 1, 2, 3, 4, 5, 6, 7, 8}
	f, err := ReadFrame(bytes.NewReader(versionFrameV2(
		CapDStar|CapDMR|CapYSF|CapM17, CapPOCSAG, byte(CPUST), udid, desc)))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVersion(f)
	if err != nil {
		t.Fatal(err)
	}
	if v.Description != desc {
		t.Errorf("Description = %q, want %q", v.Description, desc)
	}
	if v.CPU != CPUST {
		t.Errorf("CPU = %v, want ST", v.CPU)
	}
	// An ST part reports 96 bits; the layout's remaining four bytes are padding
	// and must not be reported as part of the chip's identity.
	if want := "DEADBEEF0102030405060708"; v.UDID != want {
		t.Errorf("UDID = %q, want %q", v.UDID, want)
	}
	m := v.Modes()
	if !m.Known {
		t.Error("protocol-2 capabilities must be reported as known")
	}
	if !m.DStar || !m.DMR || !m.YSF || !m.M17 || !m.POCSAG {
		t.Errorf("capabilities decoded as %+v", m)
	}
	if m.P25 || m.NXDN || m.FM {
		t.Errorf("modes decoded as present that were not set: %+v", m)
	}
}

func TestProtocol1CapabilitiesAreMarkedAssumed(t *testing.T) {
	// MMDVM-Host fills in the classic five plus POCSAG for protocol-1 firmware.
	// Waypoint mirrors it so the two agree — but the whole point of reading
	// capabilities is to refuse a mode the board cannot do, and a guess must
	// never be allowed to masquerade as a fact.
	m := Version{Protocol: 1}.Modes()
	if m.Known {
		t.Fatal("protocol-1 capabilities must not be reported as known")
	}
	if !m.DStar || !m.DMR || !m.YSF || !m.P25 || !m.NXDN || !m.POCSAG {
		t.Errorf("protocol-1 assumption drifted from MMDVM-Host's: %+v", m)
	}
	if m.M17 || m.FM {
		t.Errorf("protocol-1 must not claim modes MMDVM-Host does not assume: %+v", m)
	}
}

func TestParseVersionRejectsOtherFrames(t *testing.T) {
	// Reading someone else's traffic is the signature of probing a port
	// MMDVM-Host still owns. It must be a distinguishable error.
	f := Frame{Type: 0x01, Payload: []byte{1, 2, 3}}
	if _, err := ParseVersion(f); !errors.Is(err, ErrNotVersion) {
		t.Fatalf("err = %v, want ErrNotVersion", err)
	}
}

func TestParseVersionRejectsUnsupportedProtocol(t *testing.T) {
	_, err := ParseVersion(Frame{Type: cmdGetVersion, Payload: []byte{9}})
	if err == nil {
		t.Fatal("accepted an unsupported protocol version")
	}
	if !strings.Contains(err.Error(), "9") {
		t.Errorf("err = %v; it should name the version it could not handle", err)
	}
}

func TestDescriptionIsSanitised(t *testing.T) {
	// Firmware pads the string with NULs, and the result is stored, exported in
	// a profile fingerprint and rendered in a browser. Control bytes must not
	// leave this package.
	raw := append([]byte("ZUMspot-v1.5.2"), 0x00, 0x00, 0x07, 0x00)
	f, err := ReadFrame(bytes.NewReader(versionFrameV1(string(raw))))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVersion(f)
	if err != nil {
		t.Fatal(err)
	}
	if v.Description != "ZUMspot-v1.5.2" {
		t.Errorf("Description = %q, want the padding stripped", v.Description)
	}
}

func TestModesSupportsUsesConfigSectionKeys(t *testing.T) {
	// The names here are compared against the config store's modes section, so
	// they must be the store's keys and not a second vocabulary.
	m := Modes{Known: true, DMR: true}
	if !m.Supports("dmr") {
		t.Error(`Supports("dmr") = false`)
	}
	if m.Supports("ysf") || m.Supports("nonsense") {
		t.Error("Supports matched a mode that is not enabled or not real")
	}
}
