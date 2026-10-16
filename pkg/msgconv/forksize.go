package msgconv

// Fork (weeebdev/matrix-whatsapp): keep converted events under the homeserver's event size limit.
// Matrix events are capped at 64 KiB, and encryption grows them by about a third, so one huge
// WhatsApp message (long text, big contact list, giant link preview) would otherwise be rejected,
// and in a backfill batch it fails the whole batch forever.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

// ForkMaxContentBytes is the plaintext budget for one event's content. 40 KB of JSON stays under
// 64 KiB after megolm encryption (base64, about 4/3) plus the event envelope.
const ForkMaxContentBytes = 40_000

const forkTruncatedMarker = "\n\n… (message truncated: too long to bridge)"

func forkContentSize(content *event.MessageEventContent, extra map[string]any) int {
	data, err := json.Marshal(&event.Content{Parsed: content, Raw: extra})
	if err != nil {
		return 0
	}
	return len(data)
}

// ForkCapContent shrinks content (and extra, if given) in place until it fits budget bytes:
// first it drops formatted_body, then truncates body, then drops extra keys, largest first.
// Returns true if anything was changed.
func ForkCapContent(ctx context.Context, content *event.MessageEventContent, extra map[string]any, budget int) bool {
	if content == nil || forkContentSize(content, extra) <= budget {
		return false
	}
	log := zerolog.Ctx(ctx)
	before := forkContentSize(content, extra)
	if content.FormattedBody != "" {
		content.FormattedBody = ""
		content.Format = ""
	}
	for forkContentSize(content, extra) > budget && len(content.Body) > len(forkTruncatedMarker)+16 {
		over := forkContentSize(content, extra) - budget
		keep := len(content.Body) - over - len(forkTruncatedMarker) - 64
		if keep > len(content.Body)/2 {
			keep = len(content.Body) / 2
		}
		if keep < 0 {
			keep = 0
		}
		for keep > 0 && !utf8.RuneStart(content.Body[keep]) {
			keep--
		}
		content.Body = content.Body[:keep] + forkTruncatedMarker
	}
	if forkContentSize(content, extra) > budget && len(extra) > 0 {
		keys := make([]string, 0, len(extra))
		for k := range extra {
			keys = append(keys, k)
		}
		size := func(k string) int { b, _ := json.Marshal(extra[k]); return len(b) }
		sort.Slice(keys, func(i, j int) bool { return size(keys[i]) > size(keys[j]) })
		for _, k := range keys {
			delete(extra, k)
			if forkContentSize(content, extra) <= budget {
				break
			}
		}
	}
	log.Warn().
		Int("size_before", before).
		Int("size_after", forkContentSize(content, extra)).
		Str("msgtype", string(content.MsgType)).
		Msg("Shrank oversized event to fit the homeserver size limit")
	return true
}

const forkFullTextNote = "\n\n… (message truncated: full text attached as message.txt)"

// forkCapConverted applies ForkCapContent to every part of a converted message. When a part's text
// had to be truncated (not just its formatting dropped), the full text is attached as message.txt,
// like Telegram does for long messages.
func forkCapConverted(ctx context.Context, cm *bridgev2.ConvertedMessage) {
	if cm == nil {
		return
	}
	var attachments []*bridgev2.ConvertedMessagePart
	for _, part := range cm.Parts {
		if part == nil || part.Content == nil {
			continue
		}
		original := part.Content.Body
		if !ForkCapContent(ctx, part.Content, part.Extra, ForkMaxContentBytes) || !strings.HasSuffix(part.Content.Body, forkTruncatedMarker) {
			continue
		}
		if txt := forkFullTextPart(ctx, part.ID, original); txt != nil {
			part.Content.Body = strings.TrimSuffix(part.Content.Body, forkTruncatedMarker) + forkFullTextNote
			attachments = append(attachments, txt)
		}
	}
	cm.Parts = append(cm.Parts, attachments...)
}

// forkFullTextPart uploads text as message.txt and returns it as an extra file part, or nil.
func forkFullTextPart(ctx context.Context, partID networkid.PartID, text string) *bridgev2.ConvertedMessagePart {
	intent, _ := ctx.Value(contextKeyIntent).(bridgev2.MatrixAPI)
	portal, _ := ctx.Value(contextKeyPortal).(*bridgev2.Portal)
	if intent == nil || portal == nil {
		return nil
	}
	data := []byte(text)
	url, file, err := intent.UploadMedia(ctx, portal.MXID, data, "message.txt", "text/plain")
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to upload full text of truncated message")
		return nil
	}
	return &bridgev2.ConvertedMessagePart{
		ID:   networkid.PartID("fork-fulltext-" + string(partID)),
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType:  event.MsgFile,
			Body:     "message.txt",
			FileName: "message.txt",
			URL:      url,
			File:     file,
			Info:     &event.FileInfo{MimeType: "text/plain", Size: len(data)},
		},
	}
}
