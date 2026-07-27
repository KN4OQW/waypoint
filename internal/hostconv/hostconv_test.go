package hostconv

import (
	"encoding/json"
	"testing"
)

// The gateways read every field with it["key"] on a const nlohmann object, where a
// missing key is undefined behaviour rather than a default. So the test that
// matters most is not "does it round-trip" but "is every key the gateway touches
// present on every record".
var ysfKeys = []string{"designator", "country", "name", "use_xx_prefix", "user_count", "description", "port", "ipv4", "ipv6"}
var refKeys = []string{"designator", "name", "country", "sponsor", "port", "ipv4", "ipv6"}

func records(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var d struct {
		Reflectors []map[string]any `json:"reflectors"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(d.Reflectors) == 0 {
		t.Fatal("no reflectors in output")
	}
	return d.Reflectors
}

func requireKeys(t *testing.T, recs []map[string]any, keys []string) {
	t.Helper()
	for i, r := range recs {
		for _, k := range keys {
			if _, ok := r[k]; !ok {
				t.Errorf("record %d is missing %q — the gateway would read a missing key", i, k)
			}
		}
	}
}

const ysfSample = `# comment
00006;GR-HELLAS Zone A;(YCS202);46.59.68.211;42000;000;
00007;XX-EUROPELINK;Europelink;161.97.73.43;42000;000;
00030;US-NOPREFIXHERE;;1.2.3.4;42001;017;
garbage line without enough fields
`

func TestYSFEmitsEveryKeyTheGatewayReads(t *testing.T) {
	out, err := YSF([]byte(ysfSample))
	if err != nil {
		t.Fatal(err)
	}
	recs := records(t, out)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (the malformed line should be skipped)", len(recs))
	}
	requireKeys(t, recs, ysfKeys)
}

// The gateway rebuilds the display name as country + "-" + name, so the prefix
// baked into the source name has to come back off — otherwise every reflector
// renders as "GR-GR-HELLAS Zone A".
func TestYSFSplitsTheCountryPrefixBackOff(t *testing.T) {
	out, _ := YSF([]byte(ysfSample))
	recs := records(t, out)

	if recs[0]["country"] != "GR" || recs[0]["name"] != "HELLAS Zone A" {
		t.Errorf("country/name = %v/%v, want GR/HELLAS Zone A", recs[0]["country"], recs[0]["name"])
	}
	if recs[0]["use_xx_prefix"] != false {
		t.Error("a country-prefixed reflector should not claim the XX wildcard")
	}
	// XX is the wildcard, not a country: it must come through use_xx_prefix so the
	// gateway writes "XX-EUROPELINK" and not "XX-XX-EUROPELINK".
	if recs[1]["use_xx_prefix"] != true || recs[1]["country"] != "" || recs[1]["name"] != "EUROPELINK" {
		t.Errorf("XX record = %+v, want use_xx_prefix with an empty country", recs[1])
	}
}

// A blank description is null, not "", because the gateway null-checks it and
// substitutes the name — an empty string would render as blank instead.
func TestYSFBlankDescriptionIsNull(t *testing.T) {
	out, _ := YSF([]byte(ysfSample))
	recs := records(t, out)
	if recs[2]["description"] != nil {
		t.Errorf("description = %v, want null", recs[2]["description"])
	}
	if recs[0]["description"] != "(YCS202)" {
		t.Errorf("description = %v, want the source text", recs[0]["description"])
	}
}

func TestYSFPortIsANumberAndCountIsAString(t *testing.T) {
	out, _ := YSF([]byte(ysfSample))
	recs := records(t, out)
	// The gateway reads port as unsigned short and user_count as std::string;
	// swapping the JSON types throws inside nlohmann.
	if _, ok := recs[0]["port"].(float64); !ok {
		t.Errorf("port is %T, want a JSON number", recs[0]["port"])
	}
	if _, ok := recs[0]["user_count"].(string); !ok {
		t.Errorf("user_count is %T, want a JSON string", recs[0]["user_count"])
	}
	if _, ok := recs[0]["designator"].(string); !ok {
		t.Errorf("YSF designator is %T, want a JSON string", recs[0]["designator"])
	}
}

func TestYSFAlwaysEmitsIPv6AsNull(t *testing.T) {
	out, _ := YSF([]byte(ysfSample))
	for i, r := range records(t, out) {
		v, ok := r["ipv6"]
		if !ok || v != nil {
			t.Errorf("record %d ipv6 = %v (present=%v), want an explicit null", i, v, ok)
		}
	}
}

const p25Hosts = `# comment
38	p2538.freeddns.org	41002
49	1.2.3.4	41000
bad
`
const p25TG = `9999;P25-ULINK;P25 UNLINK
38;P25-REF-38;P25-REF-38
`

func TestP25TakesNamesFromTheCompanionList(t *testing.T) {
	out, err := P25([]byte(p25Hosts), []byte(p25TG))
	if err != nil {
		t.Fatal(err)
	}
	recs := records(t, out)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	requireKeys(t, recs, refKeys)
	if recs[0]["name"] != "P25-REF-38" {
		t.Errorf("name = %v, want the TGList name", recs[0]["name"])
	}
	// A hostname is fine: the gateway resolves the field with CUDPSocket::lookup.
	if recs[0]["ipv4"] != "p2538.freeddns.org" {
		t.Errorf("ipv4 = %v, want the address as written", recs[0]["ipv4"])
	}
	if _, ok := recs[0]["designator"].(float64); !ok {
		t.Errorf("P25 designator is %T, want a JSON number", recs[0]["designator"])
	}
}

// The hostlist carries no names at all, so without a TGList the picker would show
// bare numbers. Falling back to a synthesised label keeps it readable.
func TestNumberedFallsBackToASyntheticName(t *testing.T) {
	out, err := P25([]byte(p25Hosts), nil)
	if err != nil {
		t.Fatal(err)
	}
	recs := records(t, out)
	if recs[0]["name"] != "P25-38" {
		t.Errorf("name = %v, want a synthesised label", recs[0]["name"])
	}
	if out2, err := NXDN([]byte("14\tnxdn.c4fm.es\t41401\n"), nil); err != nil {
		t.Fatal(err)
	} else if r := records(t, out2); r[0]["name"] != "NXDN-14" {
		t.Errorf("NXDN name = %v", r[0]["name"])
	}
}

// A source that returns something unparseable must fail loudly rather than
// publish an empty list that would wipe every picker.
func TestEmptyInputIsAnError(t *testing.T) {
	if _, err := YSF([]byte("# nothing here\n")); err == nil {
		t.Error("YSF: want an error for a list with no records")
	}
	if _, err := P25([]byte(""), nil); err == nil {
		t.Error("P25: want an error for a list with no records")
	}
	if _, err := NXDN([]byte("garbage\n"), nil); err == nil {
		t.Error("NXDN: want an error for a list with no records")
	}
}
