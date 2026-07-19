package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config import / migration from an incumbent Pi-Star or WPSD card (RFC-0007 /
// issue #4). Reads the incumbent's daemon config files — from a mounted directory
// or uploaded bytes — maps them into a Model via the existing seed-path mapper
// (fromINI), and reports what could not be mapped. The store is authoritative
// after a one-time bulk write; the incumbent files are never read again.

// Migration roles — one per incumbent daemon config file. The string values are
// stable identifiers surfaced in the API/report.
const (
	roleMMDVM         = "mmdvm"
	roleDMRGateway    = "dmrgateway"
	roleYSFGateway    = "ysfgateway"
	roleDGIdGateway   = "dgidgateway"
	roleP25Gateway    = "p25gateway"
	roleNXDNGateway   = "nxdngateway"
	roleDStarGateway  = "dstargateway"
	roleM17Gateway    = "m17gateway"
	roleDAPNETGateway = "dapnetgateway"
)

// migrationRoles is the ordered role list (drives the report's file checklist).
var migrationRoles = []string{
	roleMMDVM, roleDMRGateway, roleYSFGateway, roleDGIdGateway,
	roleP25Gateway, roleNXDNGateway, roleDStarGateway, roleM17Gateway, roleDAPNETGateway,
}

// incumbentFiles maps each role to the filenames it may appear under. Pi-Star
// stores configs in /etc with lowercase, extension-less names (mmdvmhost,
// dmrgateway, …); WPSD and generic exports use the .ini-suffixed forms. Matching
// is case-insensitive, so a single table covers both.
var incumbentFiles = map[string][]string{
	roleMMDVM:         {"mmdvmhost", "mmdvm-host.ini", "mmdvmhost.ini"},
	roleDMRGateway:    {"dmrgateway", "dmrgateway.ini"},
	roleYSFGateway:    {"ysfgateway", "ysfgateway.ini"},
	roleDGIdGateway:   {"dgidgateway", "dgidgateway.ini"},
	roleP25Gateway:    {"p25gateway", "p25gateway.ini"},
	roleNXDNGateway:   {"nxdngateway", "nxdngateway.ini"},
	roleDStarGateway:  {"dstargateway", "dstargateway.cfg", "dstargateway.ini"},
	roleM17Gateway:    {"m17gateway", "m17gateway.ini"},
	roleDAPNETGateway: {"dapnetgateway", "dapnetgateway.ini"},
}

// MigrationReport is what a scan returns alongside the mapped model: what platform
// the card looks like, which files were found, which modes/networks imported, and
// — the #4 requirement — what could not be mapped.
type MigrationReport struct {
	Platform string          `json:"platform"` // "Pi-Star …", "WPSD …", or "unknown"
	Files    []FileStatus    `json:"files"`
	Modes    []string        `json:"modes"` // enabled mode names, in display order
	Networks []NetworkStatus `json:"networks"`
	Unmapped []UnmappedItem  `json:"unmapped"`
}

// FileStatus is one incumbent config file: found (with the name it was found
// under) or missing.
type FileStatus struct {
	Role  string `json:"role"`
	Name  string `json:"name,omitempty"`
	Found bool   `json:"found"`
}

// NetworkStatus is an imported DMR network and how it classified. Custom means its
// routing was hand-tuned and preserved verbatim rather than regenerated from a
// clean WPSD type — imported, but flagged so the operator knows it wasn't
// normalized.
type NetworkStatus struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Custom  bool   `json:"custom"`
	Enabled bool   `json:"enabled"`
}

// UnmappedItem is an incumbent feature Waypoint does not model — named so the
// operator can reconfigure it natively rather than discover it missing later.
type UnmappedItem struct {
	File    string `json:"file"`
	Section string `json:"section"`
	What    string `json:"what"`
}

// unmappedChecks is the curated set of incumbent features Waypoint does not carry
// into the store. Each fires when its section is present (and, if key is set, that
// key equals enable) in the given role's file. Coarse enough to maintain, specific
// enough to act on.
var unmappedChecks = []struct {
	role, section, key, enable, what string
}{
	{roleMMDVM, "APRS", "Enable", "1", "APRS position reporting (MMDVM-Host)"},
	{roleMMDVM, "Remote Commands", "Enable", "1", "Remote command UDP interface"},
	{roleMMDVM, "Mobile GPS", "Enable", "1", "Mobile GPS location source"},
	{roleMMDVM, "Lock File", "Enable", "1", "MMDVM lock file"},
	{roleMMDVM, "Transparent Data", "Enable", "1", "Transparent data passthrough"},
	{roleYSFGateway, "APRS", "Enable", "1", "YSF APRS gateway"},
	{roleP25Gateway, "APRS", "Enable", "1", "P25 APRS gateway"},
	{roleNXDNGateway, "APRS", "Enable", "1", "NXDN APRS gateway"},
	{roleDStarGateway, "APRS", "Enable", "1", "D-Star APRS"},
}

// Locate reads an incumbent card's config files from a mounted directory. It
// searches dir and dir/etc (a card's root partition presents configs at
// <mount>/etc/…) for each role's candidate names, and detects the platform from a
// release marker. The returned map is keyed by role; a missing role is simply
// absent. Only MMDVM-Host being absent is fatal downstream (Migrate).
func Locate(dir string) (contents map[string][]byte, names map[string]string, platform string, err error) {
	searchDirs := []string{dir, filepath.Join(dir, "etc")}
	contents = map[string][]byte{}
	names = map[string]string{}
	for _, role := range migrationRoles {
		for _, sd := range searchDirs {
			name, path, ok := findInDir(sd, incumbentFiles[role])
			if !ok {
				continue
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil, nil, "", fmt.Errorf("read %s: %w", path, rerr)
			}
			contents[role] = b
			names[role] = name
			break
		}
	}
	platform = detectPlatform(searchDirs)
	return contents, names, platform, nil
}

// findInDir returns the first directory entry whose name case-insensitively
// matches one of candidates. A candidate earlier in the list wins, so the
// canonical name is preferred over an alias.
func findInDir(dir string, candidates []string) (name, path string, ok bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", false
	}
	have := map[string]string{} // lowercased name -> actual name
	for _, e := range entries {
		if !e.IsDir() {
			have[strings.ToLower(e.Name())] = e.Name()
		}
	}
	for _, c := range candidates {
		if actual, found := have[strings.ToLower(c)]; found {
			return actual, filepath.Join(dir, actual), true
		}
	}
	return "", "", false
}

// detectPlatform reads a release marker if present. Informational only — the same
// mapping serves both platforms; nothing branches on this.
func detectPlatform(dirs []string) string {
	markers := []struct{ file, label string }{
		{"pistar-release", "Pi-Star"},
		{"wpsd-release", "WPSD"},
		{"WPSD_VERSION", "WPSD"},
	}
	for _, d := range dirs {
		for _, m := range markers {
			if _, path, ok := findInDir(d, []string{m.file}); ok {
				if b, err := os.ReadFile(path); err == nil {
					if v := releaseVersion(string(b)); v != "" {
						return m.label + " " + v
					}
				}
				return m.label
			}
		}
	}
	return "unknown"
}

// releaseVersion pulls a version-looking token from a release marker's contents
// (e.g. Pi-Star's "Version = 4.2.1" line).
func releaseVersion(s string) string {
	for _, line := range strings.Split(s, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "version") {
			if i := strings.IndexAny(line, "=:"); i >= 0 {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}

// RoleForFilename maps an uploaded file's name to a migration role (case- and
// path-insensitive), or "" if it is not a recognized incumbent config file. The
// upload path uses this so an uploaded set maps exactly like a mounted scan.
func RoleForFilename(name string) string {
	base := strings.ToLower(filepath.Base(name))
	for _, role := range migrationRoles {
		for _, c := range incumbentFiles[role] {
			if base == strings.ToLower(c) {
				return role
			}
		}
	}
	return ""
}

// Migrate maps an incumbent card's config (role -> file bytes) into a Model and a
// report. MMDVM-Host is required; every other role is optional (a missing gateway
// takes its documented defaults, exactly as the seed path handles absent files).
// It writes nothing — the caller previews the report/model, then commits.
func Migrate(contents map[string][]byte, names map[string]string, platform string) (*Model, *MigrationReport, error) {
	parsed := map[string]*INI{}
	for role, b := range contents {
		ini, err := ParseINI(strings.NewReader(string(b)))
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", role, err)
		}
		parsed[role] = ini
	}
	mm := parsed[roleMMDVM]
	if mm == nil {
		return nil, nil, fmt.Errorf("no MMDVM-Host config found — this does not look like a Pi-Star/WPSD card")
	}

	// Reuse the seed-path mapper: it maps identity, frequencies, modem, all eight
	// modes, the DMR network list (with WPSD-type classification and verbatim-rewrite
	// preservation), and every gateway. Absent roles pass nil → documented defaults.
	m := fromINI(
		mm,
		parsed[roleDMRGateway],
		parsed[roleYSFGateway],
		parsed[roleDGIdGateway],
		parsed[roleP25Gateway],
		parsed[roleNXDNGateway],
		parsed[roleDStarGateway],
		parsed[roleM17Gateway],
		parsed[roleDAPNETGateway],
	)

	report := buildReport(m, parsed, names, platform)
	return m, report, nil
}

func buildReport(m *Model, parsed map[string]*INI, names map[string]string, platform string) *MigrationReport {
	r := &MigrationReport{Platform: platform}

	for _, role := range migrationRoles {
		fs := FileStatus{Role: role}
		if _, ok := parsed[role]; ok {
			fs.Found = true
			fs.Name = names[role]
		}
		r.Files = append(r.Files, fs)
	}

	for _, md := range []struct {
		on   bool
		name string
	}{
		{m.Modes.DStar, "D-Star"}, {m.Modes.DMR, "DMR"}, {m.Modes.YSF, "System Fusion"},
		{m.Modes.P25, "P25"}, {m.Modes.NXDN, "NXDN"}, {m.Modes.M17, "M17"},
		{m.Modes.POCSAG, "POCSAG"}, {m.Modes.FM, "FM"},
	} {
		if md.on {
			r.Modes = append(r.Modes, md.name)
		}
	}

	for _, n := range m.Networks {
		r.Networks = append(r.Networks, NetworkStatus{
			Name: n.Name, Type: string(n.Type), Custom: n.Type == NetCustom, Enabled: n.Enabled,
		})
	}

	for _, c := range unmappedChecks {
		ini := parsed[c.role]
		if ini == nil || !ini.Has(c.section) {
			continue
		}
		if c.key != "" && ini.Get(c.section, c.key) != c.enable {
			continue
		}
		r.Unmapped = append(r.Unmapped, UnmappedItem{File: c.role, Section: c.section, What: c.what})
	}
	sort.SliceStable(r.Unmapped, func(i, j int) bool {
		if r.Unmapped[i].File != r.Unmapped[j].File {
			return r.Unmapped[i].File < r.Unmapped[j].File
		}
		return r.Unmapped[i].Section < r.Unmapped[j].Section
	})
	return r
}
