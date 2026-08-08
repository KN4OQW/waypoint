# Waypoint wiki

Waypoint is a modern, MQTT-native host for MMDVM digital-voice hotspots and
repeaters. The **[repository README](https://github.com/KN4OQW/waypoint)** is the
place to start: what it does, and the design documents behind each feature.

This wiki carries the pages an operator wants at the radio — including the ones
they need when they *cannot* reach the documentation on their own node.

## Pages

- **[Basic setup](Basic-setup)** — flashing the SD-card image, verifying the
  download, first boot, and the setup wizard that gives the node its name, its
  recovery account and its network. Start here with new hardware.
- **[Regaining access](Regaining-access)** — locked out of your dashboard? A
  Waypoint password cannot be recovered, but you can take the node back with a
  shell on it or with its SD card in a reader.
- **[Text messaging](Text-messaging)** — send a DMR text message to a radio over
  your own RF, with nothing upstream involved. Starts with the radio setting that
  catches everybody.
- **[Text messaging API](Text-messaging-API)** — the authenticated REST surface,
  for a club bot or a script.
- **[Translating Waypoint](Translating-Waypoint)** — the UI reads its text from
  JSON catalogs, so adding or fixing a language is a pull request that touches one
  file. What the rules are, and how to try it without hardware.

## Where things live

| Looking for | Go to |
|---|---|
| The images themselves | [the latest release](https://github.com/KN4OQW/waypoint/releases/latest) |
| Setting up a batch of nodes from the boot partition | [docs/provisioning.md](https://github.com/KN4OQW/waypoint/blob/main/docs/provisioning.md) |
| What Waypoint covers vs Pi-Star / WPSD | [docs/pistar-parity.md](https://github.com/KN4OQW/waypoint/blob/main/docs/pistar-parity.md) |
| Everything that is built, in detail | [docs/features.md](https://github.com/KN4OQW/waypoint/blob/main/docs/features.md) |
| Why something is built the way it is | [RFCs, in Discussions](https://github.com/KN4OQW/waypoint/discussions) |
| A bug, or a feature you want | [Issues](https://github.com/KN4OQW/waypoint/issues) |

Every page here is generated from a file in the repository — each says which one
at the top, and edits made in this browser are overwritten the next time that file
changes. Send a pull request against the source instead.
