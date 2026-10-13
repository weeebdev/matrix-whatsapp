# weeebdev/matrix-whatsapp

A small patch set on top of [mautrix/whatsapp](https://github.com/mautrix/whatsapp). Everything else is upstream.

New bot commands (send them to the bridge bot):

- `invisible on|off` (default off): don't send read receipts or typing notifications to WhatsApp.
  Unlike WhatsApp's own read-receipt privacy setting, you still see other people's read receipts.
- `keep-deleted on|off` (default on): when someone deletes a message for everyone, keep it in Matrix,
  struck through and marked "🗑 deleted on WhatsApp", with attachments kept. Your own deletes still
  remove the message.

The code lives in `pkg/connector/privacy.go`, plus a few one-line hooks in `events.go`,
`handlematrix.go`, `connector.go` and `waid/dbmeta.go`.

Images: `ghcr.io/weeebdev/matrix-whatsapp`, tagged `<upstream release>-weeebdev.N` (e.g.
`v0.2609.0-weeebdev.1`) and `latest`. The Go module path and the binary path
(`/usr/bin/mautrix-whatsapp`) stay as upstream, so the image is a drop-in for the official one.

Updates are automatic: `.github/workflows/sync-upstream.yml` runs daily. When upstream tags a new
release, it rebases the fork commits onto that tag, runs the tests, pushes `privacy` and a
`<release>-weeebdev.1` tag, and builds the image. On a conflict or test failure the run fails
(GitHub emails the repo owner) and nothing is published; fix by rebasing manually:
`git fetch upstream --tags && git rebase --onto <release> $(git merge-base privacy upstream/main)`.
