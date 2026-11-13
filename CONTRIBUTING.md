# Contributing

Thanks for your interest! Issues and pull requests are welcome. This plugin
follows the conventions of the main project; see its
[contributing guide](https://github.com/cestila/ConduitOmniChat/blob/main/CONTRIBUTING.md)
and [plugin documentation](https://github.com/cestila/ConduitOmniChat/blob/main/docs/PLUGINS.md).

## Setup and tests

```sh
go build -o conduit-plugin-telegram .
gofmt -l .        # must print nothing
go vet ./...
go test ./...
```

To try the plugin, copy the binary into a `conduitd` plugin directory (for
example `~/.local/share/conduit/plugins/`) and restart `conduitd`. To develop
against a local SDK checkout, use a local `go.work` (never commit it).

## Pull requests

`main` is protected: nobody pushes to it directly, maintainers included.

1. Create a branch from `main` and open a pull request; the template lists what
   to check.
2. CI (`test`) must pass and the branch must be up to date with `main`.
3. Pull requests are **squash merged**: the PR title becomes the commit
   message, so it must follow [Conventional Commits](https://www.conventionalcommits.org).

## Changelog

`CHANGELOG.md` follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Every pull request with a user-visible change adds a line under
**[Unreleased]**. A release is a pull request that moves those entries under
the new version; after merging it, tag `vX.Y.Z` on `main` and push the tag to
publish the binaries.

By contributing you agree your work is licensed under Apache-2.0.
