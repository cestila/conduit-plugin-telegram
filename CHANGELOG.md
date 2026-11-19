# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Every pull request with a user-visible change adds an entry under
**[Unreleased]**; a release moves those entries under its version.

## [Unreleased]

## [0.1.3] - 2026-11-19

### Added

- Implemented `SyncContacts` RPC to fetch contacts via MTProto when connected and return cached 1:1 human contacts (excluding bots, self, deleted users, groups, and channels), with unit tests.

## [0.1.2] - 2026-11-19

### Added

- `channel.disconnected` event emission when the MTProto client session terminates unexpectedly.
- Updated `github.com/cestila/ConduitOmniChat/sdk` to `v0.2.0`.

## [0.1.1] - 2026-11-13

First release with published binaries. Requires a `conduitd` that speaks
plugin protocol version 1 (SDK `v0.1.0`).

### Fixed

- The release workflow limits GoReleaser to two parallel builds: the v0.1.0
  build ran out of memory and published no binaries.
- `media.go` is formatted the same by the `gofmt` of Go 1.26 and Go 1.27, so
  CI passes.

## [0.1.0] - 2026-11-13 [YANKED]

Tagged, but the release build failed and no binaries were published. Use
0.1.1, which has the same features.

### Added

- Telegram user-account login by QR code over MTProto (gotd/td), with your own
  `api_id`/`api_hash` and optional cloud password (2FA).
- Text messages, media and voice notes, replies, reactions, polls, buttons,
  edit, delete, pin, read sync and forum topics.
- Plugin manifest (`GetManifest`) so the desktop app renders the account form
  and QR instructions.

[Unreleased]: https://github.com/cestila/conduit-plugin-telegram/compare/v0.1.3...HEAD
[0.1.3]: https://github.com/cestila/conduit-plugin-telegram/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/cestila/conduit-plugin-telegram/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/cestila/conduit-plugin-telegram/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/cestila/conduit-plugin-telegram/releases/tag/v0.1.0
