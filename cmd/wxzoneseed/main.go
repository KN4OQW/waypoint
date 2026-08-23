// Command wxzoneseed refreshes the county table shipped in the binary.
//
// internal/wxzones/seed/counties.txt is what the Weather panel's county picker
// searches. Unlike the hostlists under internal/hostsrc/seed it is not a floor
// under a download — nothing refreshes it at runtime, by design. A Waypoint
// device contacts project infrastructure only to check for updates and refresh
// public host/ID databases (GOVERNANCE.md principle 2), and a county table that
// changes a handful of times a decade does not justify a third outbound request
// on every node to carry changes that arrive that slowly. So the shipped copy is
// the whole of it, and this command is how it moves.
//
// Run it when cutting a release and read the diff before committing:
//
//	go run ./cmd/wxzoneseed             # rewrite the shipped copy
//	go run ./cmd/wxzoneseed -check      # report staleness, change nothing
//
// # Where the columns come from
//
// api.weather.gov/zones?type=county is authoritative and current — it is the
// same service the alerts come from, and its records carry effective dates. It
// gives the UGC, the name, the state and the forecast office, but NOT the SAME
// code the alert feed keys on. SAME is derived: "0" + state FIPS + the UGC's
// three digits.
//
// That derivation is not assumed, it is measured, in both directions and against
// two independent sources (CLAUDE.md: reading a source is not evidence):
//
//   - Against NWS's own published SAME list (weather.gov/source/nwr/SameCode.txt,
//     last modified 2016-10-03), 3246 of 3269 county zones derive to a code that
//     is in the list under the same county. All 23 that do not are the 2016 file
//     being out of date, not the derivation being wrong: 8 Alaska boroughs and
//     census areas from the reorganisations after it was written (Chugach, Copper
//     River, Kusilvak, Hoonah-Angoon, Petersburg, Prince of Wales-Hyder, Skagway,
//     Wrangell), Oglala Lakota in South Dakota — which is Shannon County renamed
//     in 2015, and still listed under its old code 046113 there — and 14
//     Federated States of Micronesia island zones. That is why the file is used
//     as a cross-check here and not as the source.
//
//   - Against live CAP alerts (api.weather.gov/alerts/active), which carry BOTH
//     geocodes for the same area. Every county UGC in every active alert derived
//     to a SAME code that was present in that alert's own SAME list: 132 of 132
//     pairs across 42 alerts, sampled 2026-08-23. This is the check that matters,
//     because it tests the derivation against the exact field the feed keys on.
//
// -check re-runs the SameCode.txt cross-check and reports it, so a future capture
// that breaks the derivation is loud rather than silent.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	zonesURL    = "https://api.weather.gov/zones?type=county"
	sameCodeURL = "https://www.weather.gov/source/nwr/SameCode.txt"
	seedPath    = "internal/wxzones/seed/counties.txt"

	// The NWS API asks for a contact in the User-Agent and throttles anonymous
	// clients. This runs on a maintainer's machine at release time, never on a
	// node, so naming the project here identifies no device.
	userAgent = "waypoint-wxzoneseed (https://github.com/KN4OQW/waypoint)"
)

func main() {
	check := flag.Bool("check", false, "report staleness and the cross-check; write nothing")
	out := flag.String("out", seedPath, "file to write")
	flag.Parse()

	zones, err := fetchZones()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wxzoneseed: %v\n", err)
		os.Exit(1)
	}
	// The SAME list doubles as the state -> FIPS table. Taking it from NWS's own
	// file rather than typing 60 pairs out means the mapping and the cross-check
	// rest on the same source, and a hand-typed digit cannot be wrong in only one
	// of them.
	sameByCode, fipsByState, err := fetchSameCodes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wxzoneseed: %v\n", err)
		os.Exit(1)
	}

	rows, unknown := build(zones, fipsByState)
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "wxzoneseed: %d zone(s) in states with no FIPS in %s: %v\n", len(unknown), sameCodeURL, unknown)
		os.Exit(1)
	}

	agree, disagree := crossCheck(rows, sameByCode)
	fmt.Printf("counties: %d\n", len(rows))
	fmt.Printf("SAME cross-check against %s: %d agree, %d not in that list\n", sameCodeURL, agree, len(disagree))
	for _, d := range disagree {
		fmt.Printf("  not in the 2016 list: %s %s, %s\n", d.SAME, d.Name, d.State)
	}

	body := render(rows)
	old, _ := os.ReadFile(*out)
	if bytes.Equal(old, body) {
		fmt.Println("shipped copy is up to date")
		return
	}
	if *check {
		fmt.Fprintf(os.Stderr, "wxzoneseed: %s is stale (%d bytes -> %d); run `go run ./cmd/wxzoneseed`\n", *out, len(old), len(body))
		os.Exit(1)
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "wxzoneseed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", *out, len(body))
}

// row is one shipped county, in the column order of the seed file.
type row struct {
	SAME  string
	UGC   string
	Name  string
	State string
	WFO   string
}

type zone struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	State string   `json:"state"`
	CWA   []string `json:"cwa"`
}

func fetchZones() ([]zone, error) {
	var doc struct {
		Features []struct {
			Properties zone `json:"properties"`
		} `json:"features"`
	}
	if err := getJSON(zonesURL, &doc); err != nil {
		return nil, err
	}
	if len(doc.Features) == 0 {
		return nil, fmt.Errorf("%s returned no county zones", zonesURL)
	}
	zs := make([]zone, 0, len(doc.Features))
	for _, f := range doc.Features {
		zs = append(zs, f.Properties)
	}
	return zs, nil
}

// fetchSameCodes returns NWS's published SAME list keyed by code, and the state
// -> FIPS mapping implied by it.
func fetchSameCodes() (map[string]string, map[string]string, error) {
	b, err := get(sameCodeURL)
	if err != nil {
		return nil, nil, err
	}
	byCode := map[string]string{}
	fips := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ",")
		if len(parts) < 3 {
			continue
		}
		code := strings.TrimSpace(parts[0])
		name := strings.TrimSpace(parts[1])
		st := strings.TrimSpace(parts[2])
		if len(code) != 6 || st == "" {
			continue
		}
		byCode[code] = name
		// Every state in that file carries exactly one FIPS prefix; a second one
		// would mean the file changed shape under us, so say so rather than pick.
		if prev, ok := fips[st]; ok && prev != code[1:3] {
			return nil, nil, fmt.Errorf("state %s has two FIPS prefixes in %s: %s and %s", st, sameCodeURL, prev, code[1:3])
		}
		fips[st] = code[1:3]
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if len(fips) == 0 {
		return nil, nil, fmt.Errorf("%s parsed to no rows", sameCodeURL)
	}
	return byCode, fips, nil
}

// build turns the zone records into shipped rows, deriving SAME. It returns the
// ids it could not place so the caller can refuse rather than ship a short table.
func build(zones []zone, fipsByState map[string]string) ([]row, []string) {
	var rows []row
	var unknown []string
	for _, z := range zones {
		if len(z.ID) != 6 || z.ID[2] != 'C' {
			continue // not a county zone
		}
		fips, ok := fipsByState[z.State]
		if !ok {
			unknown = append(unknown, z.ID)
			continue
		}
		wfo := ""
		if len(z.CWA) > 0 {
			// 17 counties span two forecast offices. The field is shown, never
			// routed on, so the first is enough and picking it is stable.
			wfo = z.CWA[0]
		}
		rows = append(rows, row{
			SAME:  "0" + fips + z.ID[3:],
			UGC:   z.ID,
			Name:  strings.TrimSpace(z.Name),
			State: z.State,
			WFO:   wfo,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SAME < rows[j].SAME })
	return rows, unknown
}

func crossCheck(rows []row, sameByCode map[string]string) (int, []row) {
	agree := 0
	var disagree []row
	for _, r := range rows {
		if _, ok := sameByCode[r.SAME]; ok {
			agree++
			continue
		}
		disagree = append(disagree, r)
	}
	return agree, disagree
}

// render writes the pipe-delimited table. Pipe rather than comma because county
// names contain commas nowhere but apostrophes and periods everywhere, and a
// format with no quoting rules cannot be parsed two ways.
func render(rows []row) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# NWS county table for the weather alert picker. Generated by cmd/wxzoneseed.\n")
	fmt.Fprintf(&b, "# SAME|UGC|Name|State|WFO   see internal/wxzones/seed/README.md\n")
	fmt.Fprintf(&b, "# Captured %s from %s\n", time.Now().UTC().Format("2006-01-02"), zonesURL)
	for _, r := range rows {
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s\n", r.SAME, r.UGC, r.Name, r.State, r.WFO)
	}
	return b.Bytes()
}

func getJSON(url string, v any) error {
	b, err := get(url)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func get(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/geo+json,application/json,text/plain")
	cl := &http.Client{Timeout: 120 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: %s", url, resp.Status)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return buf.Bytes(), nil
}
