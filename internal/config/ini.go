// Package config reads the digital-voice daemons' INI files into a structured
// view for the dashboard. It is deliberately read-only for now: the settings
// page renders the node's real configuration instead of hard-coded values,
// while the write path (regenerate + validate + restart) lands with the
// configuration store (see waypoint#1 / waypoint#29).
package config

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// INI is a parsed INI file: section name -> key -> value. Section and key
// lookups are case-insensitive via the accessor methods; keys keep their
// original spelling for round-tripping later.
type INI struct {
	sections map[string]map[string]string
}

// ParseINIFile reads and parses an INI file from disk.
func ParseINIFile(path string) (*INI, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseINI(f)
}

// ParseINI parses INI content. It tolerates the MMDVM-Host / DMRGateway dialect:
// `[Section]` headers, `Key=Value` pairs, `#` or `;` comments, and quoted
// values (the surrounding quotes are stripped).
func ParseINI(r io.Reader) (*INI, error) {
	ini := &INI{sections: map[string]map[string]string{}}
	cur := ""
	ini.sections[cur] = map[string]string{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := ini.sections[cur]; !ok {
				ini.sections[cur] = map[string]string{}
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		ini.sections[cur][key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return ini, nil
}

// Get returns the value of a key within a section, matched case-insensitively,
// or "" if absent.
func (i *INI) Get(section, key string) string {
	s := i.section(section)
	if s == nil {
		return ""
	}
	for k, v := range s {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// Bool reports whether a key equals "1" (the daemons' enable convention).
func (i *INI) Bool(section, key string) bool {
	return i.Get(section, key) == "1"
}

// Has reports whether a section exists.
func (i *INI) Has(section string) bool {
	return i.section(section) != nil
}

func (i *INI) section(name string) map[string]string {
	for s, kv := range i.sections {
		if strings.EqualFold(s, name) {
			return kv
		}
	}
	return nil
}
