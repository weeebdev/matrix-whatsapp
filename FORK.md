# weeebdev/matrix-whatsapp

A small patch set on top of [mautrix/whatsapp](https://github.com/mautrix/whatsapp). Everything else is upstream.

New bot commands (send them to the bridge bot):

- `invisible on|off` (default off): don't send read receipts or typing notifications to WhatsApp.
  Unlike WhatsApp's own read-receipt privacy setting, you still see other people's read receipts.
- `keep-deleted on|off` (default on): when someone deletes a message for everyone, keep it in Matrix,
  struck through and marked "🗑 deleted on WhatsApp", with attachments kept. Your own deletes still
  remove the message. Messages without stored content get a "🗑 This message was deleted" reply.

- `show-edits on|off` (default on): edited messages list their previous versions underneath
  ("✏️ was: …", newest first, up to 10). Beeper otherwise only shows the latest version.

- **Transcribe voice messages on demand:** react with 📝 on a received voice message. The reaction
  isn't sent to WhatsApp; the bridge replies with "📝 <text>". Uses any OpenAI-compatible
  `/audio/transcriptions` API: env `TRANSCRIBE_API_URL` (base URL ending in `/v1`),
  `TRANSCRIBE_API_KEY`, `TRANSCRIBE_MODEL`, optional `TRANSCRIBE_LANGUAGE`. Needs the stored content,
  so it works for voice messages from others received after this was enabled.
- **TikTok links:** a TikTok link in any received message (or one you send from your phone) is
  downloaded with yt-dlp (bundled in the image) and posted as a video reply. Up to 3 links per
  message. The bridge picks H.264 with audio under 100 MB; otherwise it takes the best version
  that fits and converts it to H.264 with ffmpeg at a bitrate targeting the limit. If nothing fits,
  it replies "🎬 TikTok video too large to bridge (N MB)". Other failures leave the link as is.

- **Oversized events:** Matrix rejects events over 64 KiB, and one such event fails a whole backfill
  batch, every retry. Converted messages are capped at 40 KB of content (about 54 KB once
  encrypted), and edits at half that: formatting is dropped first, then the text is truncated,
  then optional extras such as link previews are dropped. When the text itself is truncated, the
  complete text is attached to the same message as `message.txt` (like Telegram). Each shrink logs
  a warning (`msgconv/forksize.go`).

Stored content: keep-deleted and show-edits need other people's message content kept in the bridge
database (Beeper-style `ratchet_on_decrypt` configs make old Matrix events undecryptable, so it
can't be re-read). It's sealed with AES-256-GCM (`waid/forkseal.go`) using a key from the
`CONTENT_ENCRYPTION_KEY` environment variable (`openssl rand -base64 32`), bound to the WhatsApp
message ID, so database snapshots and backups only hold ciphertext. Your own messages aren't stored.
Without the key nothing is stored. A malformed key stops the bridge at startup. Don't rotate the key.

The code lives in `pkg/connector/privacy.go`, plus a few one-line hooks in `events.go`,
`handlematrix.go`, `connector.go`, `waid/dbmeta.go` and `msgconv/from-whatsapp.go`, plus
`waid/forkseal.go`.

Images: `ghcr.io/weeebdev/matrix-whatsapp`, tagged `<upstream release>-weeebdev.N` (e.g.
`v0.2609.0-weeebdev.1`) and `latest`. The Go module path and the binary path
(`/usr/bin/mautrix-whatsapp`) stay as upstream, so the image is a drop-in for the official one.

Updates are automatic: `.github/workflows/sync-upstream.yml` runs daily. When upstream tags a new
release, it rebases the fork commits onto that tag, runs the tests, pushes `privacy` and a
`<release>-weeebdev.1` tag, and builds the image. On a conflict or test failure the run fails
(GitHub emails the repo owner) and nothing is published; fix by rebasing manually:
`git fetch upstream --tags && git rebase --onto <release> $(git merge-base privacy upstream/main)`.
