// Package hostconv converts the classic text reflector lists into the JSON the
// pinned gateways parse.
//
// The YSF, P25 and NXDN hostlists used to come from hostfiles.refcheck.radio as
// ready-made JSON, which is why nothing ever had to convert them. That host is
// gone (#138) and the only reachable replacement — pistar.uk — serves the classic
// semicolon/whitespace text formats instead. The gateways cannot read those:
// YSFGateway, P25Gateway and NXDNGateway all parse their hostlist with
// nlohmann::json and iterate data["reflectors"].
//
// So the conversion happens once, at publish time, and hostfiles.kn4oqw.com serves
// the JSON. Nodes are unchanged: the same file still feeds both the gateway and
// the settings picker, in the same format both already expect.
//
// # The contract is stricter than it looks
//
// Each gateway reads its fields with `it["key"]` on a *const* nlohmann object.
// A missing key there is undefined behaviour, not a defaulted value — so every
// record must carry every key the gateway touches, including the ones it will
// only find null. That is why ipv6 is always emitted, and why YSF records carry
// use_xx_prefix and user_count even though nothing here has a use for them.
//
// Addresses are left as written. The gateways resolve them with
// CUDPSocket::lookup, so a hostname in the ipv4 field is fine and does not need
// resolving ahead of time — which is just as well, since the published list is
// consumed by nodes on networks with their own DNS.
package hostconv

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ysfRecord mirrors what YSFGateway/YSFReflectors.cpp reads. Pointer strings are
// the fields it null-checks; plain strings are the ones it reads unconditionally.
type ysfRecord struct {
	Designator  string  `json:"designator"`
	Name        string  `json:"name"`
	Country     string  `json:"country"`
	UseXXPrefix bool    `json:"use_xx_prefix"`
	UserCount   string  `json:"user_count"`
	Description *string `json:"description"`
	IPv4        *string `json:"ipv4"`
	IPv6        *string `json:"ipv6"`
	Port        int     `json:"port"`
}

// reflectorRecord is the P25/NXDN shape. designator is a number for these two,
// where YSF's is a string — the gateways differ, so the types do too.
type reflectorRecord struct {
	Designator int     `json:"designator"`
	Name       string  `json:"name"`
	Country    string  `json:"country"`
	Sponsor    string  `json:"sponsor"`
	IPv4       *string `json:"ipv4"`
	IPv6       *string `json:"ipv6"`
	Port       int     `json:"port"`
}

type doc struct {
	Reflectors any `json:"reflectors"`
}

// YSF converts a classic YSF_Hosts.txt into YSFHosts.json.
//
// Lines are "id;name;description;address;port;count;", and the name arrives with
// its country already prefixed — "GR-HELLAS Zone A", "XX-EUROPELINK". The gateway
// rebuilds that itself as country + "-" + name (or "XX-" + name), so the prefix
// has to be split back off here or every reflector would render with it doubled.
func YSF(text []byte) ([]byte, error) {
	out := []ysfRecord{}
	err := eachLine(text, func(line string) error {
		f := strings.Split(line, ";")
		if len(f) < 5 {
			return nil // not a record; skip rather than fail the whole list
		}
		port, err := strconv.Atoi(strings.TrimSpace(f[4]))
		if err != nil {
			return nil
		}
		country, name, useXX := splitCountry(strings.TrimSpace(f[1]))
		rec := ysfRecord{
			Designator:  strings.TrimSpace(f[0]),
			Name:        name,
			Country:     country,
			UseXXPrefix: useXX,
			UserCount:   "0",
			Port:        port,
		}
		if d := strings.TrimSpace(f[2]); d != "" {
			rec.Description = &d
		}
		if a := strings.TrimSpace(f[3]); a != "" {
			rec.IPv4 = &a
		}
		if len(f) > 5 {
			if c := strings.TrimSpace(f[5]); c != "" {
				rec.UserCount = c
			}
		}
		out = append(out, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("hostconv: no YSF records parsed")
	}
	return json.Marshal(doc{Reflectors: out})
}

// splitCountry takes "GR-HELLAS Zone A" apart into the country and the bare name
// the gateway will re-join. "XX-" is the wildcard prefix and has no country, so it
// is reported through use_xx_prefix instead.
func splitCountry(full string) (country, name string, useXX bool) {
	if len(full) > 3 && full[2] == '-' {
		prefix := full[:2]
		if prefix == "XX" {
			return "", full[3:], true
		}
		return prefix, full[3:], false
	}
	// No recognisable prefix. Claim the XX wildcard rather than leaving country
	// empty, which would have the gateway render a leading "-".
	return "", full, true
}

// P25 converts a classic P25_Hosts.txt into P25Hosts.json, taking reflector names
// from the companion TGList when one is supplied.
func P25(hosts, tglist []byte) ([]byte, error) { return numbered(hosts, tglist, "P25") }

// NXDN converts a classic NXDN_Hosts.txt into NXDNHosts.json.
func NXDN(hosts, tglist []byte) ([]byte, error) { return numbered(hosts, tglist, "NXDN") }

// numbered handles the P25/NXDN shape, which is identical in both: the hostlist is
// "<designator> <address> <port>" and carries no names at all, so the picker would
// show bare numbers. The companion TGList_*.txt is "<designator>;<name>;<desc>"
// and supplies them. A missing TGList is not an error — the reflectors still work,
// they are just labelled by number.
func numbered(hosts, tglist []byte, what string) ([]byte, error) {
	names := map[int]string{}
	if len(tglist) > 0 {
		_ = eachLine(tglist, func(line string) error {
			f := strings.Split(line, ";")
			if len(f) < 2 {
				return nil
			}
			if id, err := strconv.Atoi(strings.TrimSpace(f[0])); err == nil {
				names[id] = strings.TrimSpace(f[1])
			}
			return nil
		})
	}

	out := []reflectorRecord{}
	err := eachLine(hosts, func(line string) error {
		f := strings.Fields(line)
		if len(f) < 3 {
			return nil
		}
		id, err := strconv.Atoi(f[0])
		if err != nil {
			return nil
		}
		port, err := strconv.Atoi(f[len(f)-1])
		if err != nil {
			return nil
		}
		addr := f[len(f)-2]
		rec := reflectorRecord{Designator: id, Name: names[id], IPv4: &addr, Port: port}
		if rec.Name == "" {
			rec.Name = fmt.Sprintf("%s-%d", what, id)
		}
		out = append(out, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("hostconv: no %s records parsed", what)
	}
	return json.Marshal(doc{Reflectors: out})
}

// eachLine walks non-empty, non-comment lines. Both '#' and ';' start comments in
// these files, but ';' is also the field separator, so only a leading ';' counts.
func eachLine(b []byte, fn func(string) error) error {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return sc.Err()
}
