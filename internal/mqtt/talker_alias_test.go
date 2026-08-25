package mqtt

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// TestTranslateTalkerAlias covers the note and every way one is refused. A note is
// dropped rather than partially believed: a name with no stream id could only ever
// be attached to the wrong transmission, and a stream id with no name would name
// nobody.
func TestTranslateTalkerAlias(t *testing.T) {
	for _, c := range []struct {
		name    string
		payload string
		wantOK  bool
		want    TalkerAliasNote
	}{
		{"a note", `{"type":"talker_alias","stream_id":12648430,"src_id":3180202,"name":"waypoint dev"}`, true,
			TalkerAliasNote{Type: "talker_alias", StreamID: 12648430, SrcID: 3180202, Name: "waypoint dev"}},
		{"whitespace is trimmed", `{"type":"talker_alias","stream_id":1,"src_id":2,"name":"  Booting6228 "}`, true,
			TalkerAliasNote{Type: "talker_alias", StreamID: 1, SrcID: 2, Name: "Booting6228"}},

		{"empty payload", ``, false, TalkerAliasNote{}},
		{"invalid json", `{not json`, false, TalkerAliasNote{}},
		{"wrong type", `{"type":"bus_voice_start","stream_id":1,"src_id":2,"name":"x"}`, false, TalkerAliasNote{}},
		{"no type", `{"stream_id":1,"src_id":2,"name":"x"}`, false, TalkerAliasNote{}},
		{"no stream id", `{"type":"talker_alias","src_id":2,"name":"x"}`, false, TalkerAliasNote{}},
		{"no source id", `{"type":"talker_alias","stream_id":1,"name":"x"}`, false, TalkerAliasNote{}},
		{"no name", `{"type":"talker_alias","stream_id":1,"src_id":2,"name":"   "}`, false, TalkerAliasNote{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := TranslateTalkerAlias([]byte(c.payload))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("note = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestTalkerAliasNameKeepsItsCase: the name is a Zello display name, not a
// callsign. Nothing on this path may upper-case it.
func TestTalkerAliasNameKeepsItsCase(t *testing.T) {
	n, ok := TranslateTalkerAlias([]byte(`{"type":"talker_alias","stream_id":1,"src_id":2,"name":"Booting6228"}`))
	if !ok {
		t.Fatal("a valid note was refused")
	}
	if n.Name != "Booting6228" {
		t.Errorf("name = %q, want Booting6228", n.Name)
	}
}

// TestRouteBusMessageSendsEachTopicToOneHandler. Both live under the same
// subscription, so the topic is the only thing separating an announcement from an
// event — and an announcement reaching the hub would show up as a junk row in the
// operator's event log.
func TestRouteBusMessageSendsEachTopicToOneHandler(t *testing.T) {
	const alias = `{"type":"talker_alias","stream_id":1,"src_id":3180202,"name":"waypoint dev"}`
	const event = `{"type":"bus_voice_start","mode":"DMR","source":"KN4OQW"}`

	for _, c := range []struct {
		name       string
		topic      string
		payload    string
		wantEvents int
		wantNotes  int
	}{
		{"an announcement", "waypoint/bus/bus-1/" + config.BusTalkerAliasTopic, alias, 0, 1},
		{"a bus event", "waypoint/bus/bus-1/bus_voice_start", event, 1, 0},
		// A note on the wrong topic is an event payload as far as this branch is
		// concerned, and TranslateBusEvent refuses it — so it reaches neither.
		{"an announcement on an event topic", "waypoint/bus/bus-1/bus_voice_start", alias, 0, 0},
		// And an event body on the alias topic is refused by the note decoder.
		{"an event on the announcement topic", "waypoint/bus/bus-1/" + config.BusTalkerAliasTopic, event, 0, 0},
		// The suffix must be a whole segment: a bus whose id merely ends in the
		// word is not an announcement.
		{"a topic that only ends in the word", "waypoint/bus/bus-1/not_talker_alias", event, 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := hub.New()
			ch, _, cancel := h.Subscribe()
			defer cancel()
			notes := 0
			routeBusMessage(c.topic, []byte(c.payload), h, func(TalkerAliasNote) { notes++ })

			events := 0
			for {
				select {
				case <-ch:
					events++
					continue
				default:
				}
				break
			}
			if events != c.wantEvents {
				t.Errorf("%d hub events, want %d", events, c.wantEvents)
			}
			if notes != c.wantNotes {
				t.Errorf("%d announcements, want %d", notes, c.wantNotes)
			}
		})
	}
}

// TestRouteBusMessageWithNoTalkerAliasSink: a node with nothing to inject with
// (the DMR relay is off, so waypointd sets no callback) must drop the note rather
// than fall through to the event mapping.
func TestRouteBusMessageWithNoTalkerAliasSink(t *testing.T) {
	h := hub.New()
	ch, _, cancel := h.Subscribe()
	defer cancel()
	routeBusMessage("waypoint/bus/bus-1/"+config.BusTalkerAliasTopic,
		[]byte(`{"type":"talker_alias","stream_id":1,"src_id":2,"name":"x"}`), h, nil)
	select {
	case e := <-ch:
		t.Errorf("an announcement reached the hub as %q", e.Type)
	default:
	}
}
