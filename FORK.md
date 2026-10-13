# weeebdev/mautrix-whatsapp

A small patch set on top of [mautrix/whatsapp](https://github.com/mautrix/whatsapp). Everything else is upstream.

New bot commands (send them to the bridge bot):

- `invisible on|off` (default off): don't send read receipts or typing notifications to WhatsApp.
  Unlike WhatsApp's own read-receipt privacy setting, you still see other people's read receipts.
- `keep-deleted on|off` (default on): when someone deletes a message for everyone, keep it in Matrix,
  struck through and marked "🗑 deleted on WhatsApp", with attachments kept. Your own deletes still
  remove the message.

The code lives in `pkg/connector/privacy.go`, plus a few one-line hooks in `events.go`,
`handlematrix.go`, `connector.go` and `waid/dbmeta.go`.

Images: `ghcr.io/weeebdev/mautrix-whatsapp`, built by `.github/workflows/fork-release.yml` on
`v*-weeebdev*` tags, e.g. `v0.2609.0-weeebdev.1`.

Updating: `git fetch upstream && git rebase upstream/main`, run tests, tag, push.
