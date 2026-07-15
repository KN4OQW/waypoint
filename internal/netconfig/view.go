package netconfig

// View is the Network settings surface's read model: the Model projected for the
// API with secrets removed. It mirrors internal/config.View's contract — a Wi-Fi
// PSK never appears, only whether one is set (HasPSK), so the browser edits the
// connection list without ever handling the credential. Served alongside the live
// Status; this is the desired config, Status is the observed state.
//
// The foundation exposes the view so the write-only-secret plumbing is testable
// now; the Wi-Fi/VLAN edit surface that consumes it lands in the next slice.
type View struct {
	Hostname    string           `json:"hostname"`
	Timezone    string           `json:"timezone"`
	NTP         NTP              `json:"ntp"`
	Connections []ViewConnection `json:"connections"`
}

// ViewConnection is one connection with its PSK redacted to HasPSK.
type ViewConnection struct {
	Name        string   `json:"name"`
	Type        ConnType `json:"type"`
	Interface   string   `json:"interface"`
	Autoconnect bool     `json:"autoconnect"`
	Priority    string   `json:"priority"`
	IPv4        IPv4     `json:"ipv4"`
	SSID        string   `json:"ssid"`
	Hidden      bool     `json:"hidden"`
	Country     string   `json:"country"`
	HasPSK      bool     `json:"has_psk"`
}

// View projects the Model onto the redacted API shape.
func (m Model) View() View {
	v := View{
		Hostname: m.Hostname,
		Timezone: m.Timezone,
		NTP:      m.NTP,
	}
	for _, c := range m.Connections {
		v.Connections = append(v.Connections, ViewConnection{
			Name:        c.Name,
			Type:        c.Type,
			Interface:   c.Interface,
			Autoconnect: c.Autoconnect,
			Priority:    c.Priority,
			IPv4:        c.IPv4,
			SSID:        c.WiFi.SSID,
			Hidden:      c.WiFi.Hidden,
			Country:     c.WiFi.Country,
			HasPSK:      c.WiFi.PSK != "",
		})
	}
	return v
}
