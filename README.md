# conduit-plugin-telegram

Telegram (MTProto user account) channel for Conduit, built on [gotd/td](https://github.com/gotd/td). Supports replies, reactions, polls, buttons, edit, delete, pin, read receipts, media, voice notes and forum topics.

This is an official plugin for [ConduitOmniChat](https://github.com/cestila/ConduitOmniChat).
It is a standalone executable that `conduitd` launches and talks to over gRPC
(`conduit.v1.PluginService`). It describes its own UI through a manifest.

## Install

```sh
conduitd plugin install telegram
```

or use the **Plugins** panel of the Conduit desktop app. The binary is
downloaded from this repository's GitHub Releases and its SHA-256 checksum is
verified. See the [plugin documentation](https://github.com/cestila/ConduitOmniChat/blob/main/docs/PLUGINS.md)
for plugin directories and details.

## Credentials

Login is by QR code (Settings → Devices → Link Desktop Device). You need:

| Field | Required | Notes |
| --- | --- | --- |
| `api_id` | yes | Your own, from <https://my.telegram.org/apps> |
| `api_hash` | yes | Your own, from <https://my.telegram.org/apps> |
| `password` | no | Cloud password, only if your account has 2FA |

## Build

```sh
go build -o conduit-plugin-telegram .
```

Copy the binary into one of the plugin directories of `conduitd`. To develop
against a local checkout of the SDK, add a temporary
`replace github.com/cestila/ConduitOmniChat/sdk => ../ConduitOmniChat/sdk` to
`go.mod` (do not commit it).

## Disclaimer

Not affiliated with or endorsed by Telegram. You must register **your own** `api_id`/`api_hash`; do not share them or reuse someone else's. Automating a user account is subject to Telegram's API terms.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
