package connector

// Fork additions (weeebdev/mautrix-whatsapp): per-login "invisible" mode and keeping messages
// that are deleted for everyone. Kept in one file so rebasing onto upstream stays easy.

import (
	"context"
	"fmt"
	"html"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/msgconv"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

// forkCheckContentKey validates CONTENT_ENCRYPTION_KEY at startup. A malformed key is fatal, so
// content is never silently stored unencrypted or lost; a missing key only disables storage.
func forkCheckContentKey(bridge *bridgev2.Bridge) {
	if enabled, err := waid.ForkLoadKey(); err != nil {
		bridge.Log.WithLevel(zerolog.FatalLevel).Err(err).Msg("Invalid content encryption key")
		os.Exit(15)
	} else if !enabled {
		bridge.Log.Warn().Msg(waid.ForkContentKeyEnv + " not set: message content won't be stored, so keep-deleted and show-edits are degraded")
	}
}

func (wa *WhatsAppClient) loginMeta() *waid.UserLoginMetadata {
	return wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
}

func (wa *WhatsAppClient) isInvisible() bool {
	return wa.loginMeta().Invisible
}

func (wa *WhatsAppClient) keepDeleted() bool {
	return wa.loginMeta().KeepDeletedEnabled()
}

// revokeAsDeletedEdit reports whether a revoke should mark the message as deleted instead of
// redacting it. Revokes sent from this bridge (deletes made in Matrix) still redact.
func (evt *WAMessageEvent) revokeAsDeletedEdit() bool {
	return evt.parsedMessageType == "revoke" && evt.wa.keepDeleted() && !evt.Info.IsFromMe
}

const deletedLabel = "🗑 deleted on WhatsApp"

// convertRevokeToDeletedEdit marks a revoked message as deleted in place: text is struck through,
// media keeps its file with a marked caption. Content comes from message metadata (stored when the
// message was bridged); re-reading the Matrix event is only a fallback, because Beeper configs
// (ratchet_on_decrypt) make old events undecryptable. If neither works, a reply notice is added.
func (evt *WAMessageEvent) convertRevokeToDeletedEdit(ctx context.Context, portal *bridgev2.Portal, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	log := zerolog.Ctx(ctx)
	edit := &bridgev2.ConvertedEdit{}
	for _, part := range existing {
		meta, _ := part.Metadata.(*waid.MessageMetadata)
		if meta != nil && meta.ForkDeleted {
			continue
		}
		var content *event.MessageEventContent
		var history []string
		if stored := forkOpenStored(part); stored != nil {
			content, history = stored.Content, stored.EditHistory
		} else if orig, err := evt.wa.Main.Bridge.Bot.GetEvent(ctx, portal.MXID, part.MXID); err != nil {
			log.Debug().Err(err).Stringer("part_mxid", part.MXID).Msg("Can't fetch original event of revoked message")
		} else if c := orig.Content.AsMessage(); c != nil && c.MsgType != "" {
			content = c
			if c.NewContent != nil {
				content = c.NewContent
			}
		}
		if content == nil {
			continue
		}
		edit.ModifiedParts = append(edit.ModifiedParts, &bridgev2.ConvertedEditPart{
			Part:    part,
			Type:    event.EventMessage,
			Content: evt.wa.deletedWithHistory(content, history),
		})
		if meta != nil {
			meta.ForkDeleted = true
		}
	}
	if len(existing) == 0 {
		return nil, fmt.Errorf("%w: no parts", bridgev2.ErrIgnoringRemoteEvent)
	}
	if len(edit.ModifiedParts) > 0 {
		log.Info().Int("parts", len(edit.ModifiedParts)).Msg("keep-deleted: marked revoked message as deleted")
		return edit, nil
	}
	if meta, _ := existing[0].Metadata.(*waid.MessageMetadata); meta != nil && meta.ForkDeleted {
		return nil, fmt.Errorf("%w: already marked as deleted", bridgev2.ErrIgnoringRemoteEvent)
	}
	log.Info().Msg("keep-deleted: original content unavailable, adding a reply notice")
	if meta, _ := existing[0].Metadata.(*waid.MessageMetadata); meta != nil {
		meta.ForkDeleted = true
	}
	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{},
		AddedParts: &bridgev2.ConvertedMessage{
			ReplyTo: &networkid.MessageOptionalPartID{MessageID: existing[0].ID, PartID: &existing[0].PartID},
			Parts: []*bridgev2.ConvertedMessagePart{{
				ID:      "fork-deleted",
				Type:    event.EventMessage,
				Content: &event.MessageEventContent{MsgType: event.MsgNotice, Body: "🗑 This message was deleted on WhatsApp"},
			}},
		},
	}, nil
}

// deletedWithHistory marks content as deleted, keeping its edit history visible (show-edits).
func (wa *WhatsAppClient) deletedWithHistory(content *event.MessageEventContent, history []string) *event.MessageEventContent {
	marked := deletedContent(content)
	if wa.loginMeta().ShowEditsEnabled() && len(history) > 0 {
		marked = withEditHistory(marked, history)
	}
	msgconv.ForkCapContent(context.Background(), marked, nil, msgconv.ForkMaxContentBytes/2)
	return marked
}

func deletedContent(orig *event.MessageEventContent) *event.MessageEventContent {
	c := *orig
	c.Mentions = &event.Mentions{}
	c.RelatesTo = nil
	switch c.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		formatted := c.FormattedBody
		if c.Format != event.FormatHTML || formatted == "" {
			formatted = strings.ReplaceAll(html.EscapeString(c.Body), "\n", "<br/>")
		}
		c.Format = event.FormatHTML
		c.FormattedBody = "<del>" + formatted + "</del><br/><em>" + deletedLabel + "</em>"
		c.Body = "~~" + c.Body + "~~\n" + deletedLabel
	default:
		name := c.FileName
		if name == "" {
			name = c.Body
		}
		caption := ""
		if c.FileName != "" && c.Body != c.FileName {
			caption = c.Body + "\n"
		}
		c.FileName = name
		c.Body = caption + deletedLabel
		c.Format, c.FormattedBody = "", ""
	}
	return &c
}

func boolArg(args []string) (value, ok bool) {
	if len(args) == 0 {
		return false, false
	}
	switch strings.ToLower(args[0]) {
	case "on", "yes", "true", "1", "enable":
		return true, true
	case "off", "no", "false", "0", "disable":
		return false, true
	}
	return false, false
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func settingCommand(name, description, explain string, get func(*waid.UserLoginMetadata) bool, set func(*waid.UserLoginMetadata, bool)) *commands.FullHandler {
	return &commands.FullHandler{
		Func: func(ce *commands.Event) {
			login := ce.User.GetDefaultLogin()
			if login == nil {
				ce.Reply("You're not logged in")
				return
			}
			meta := login.Metadata.(*waid.UserLoginMetadata)
			value, ok := boolArg(ce.Args)
			if !ok {
				ce.Reply("%s is **%s**. Usage: `%s on|off`\n\n%s", name, onOff(get(meta)), name, explain)
				return
			}
			set(meta, value)
			if err := login.Save(ce.Ctx); err != nil {
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("%s is now **%s**", name, onOff(value))
		},
		Name: name,
		Help: commands.HelpMeta{
			Section:     commands.HelpSectionGeneral,
			Description: description,
			Args:        "[on|off]",
		},
		RequiresLogin: true,
	}
}

var cmdInvisible = settingCommand("invisible",
	"Don't send read receipts or typing notifications to WhatsApp",
	"When on, reading or typing in Beeper isn't reported to your contacts (no blue ticks, no \"typing…\"). "+
		"Unlike WhatsApp's own read-receipt setting, you still see other people's read receipts.",
	func(m *waid.UserLoginMetadata) bool { return m.Invisible },
	func(m *waid.UserLoginMetadata, v bool) { m.Invisible = v })

var cmdKeepDeleted = settingCommand("keep-deleted",
	"Keep messages in Beeper when they're deleted for everyone on WhatsApp",
	"When on, messages others delete for everyone stay in Beeper, struck through and marked 🗑, with "+
		"attachments kept. Messages you delete yourself are still removed.",
	func(m *waid.UserLoginMetadata) bool { return m.KeepDeletedEnabled() },
	func(m *waid.UserLoginMetadata, v bool) { m.KeepDeleted = &v })

// forkOpenStored decrypts the stored content of a bridged message part, or returns nil.
func forkOpenStored(part *database.Message) *waid.ForkStored {
	meta, ok := part.Metadata.(*waid.MessageMetadata)
	if !ok || meta.ForkSealed == "" {
		return nil
	}
	stored, err := waid.ForkOpen(waid.ForkWAMessageID(part.ID), meta.ForkSealed)
	if err != nil {
		return nil
	}
	return stored
}

// forkRenderEditHistory re-seals the edited part's content under the original message ID with the
// previous version prepended to its history, and with show-edits on, renders previous versions
// under the new text. Parts without stored content (own messages, or no key) store nothing.
func (wa *WhatsAppClient) forkRenderEditHistory(part *database.Message, editPart *bridgev2.ConvertedEditPart, prev *waid.ForkStored) {
	meta, ok := part.Metadata.(*waid.MessageMetadata)
	if !ok {
		return
	}
	if prev == nil || editPart == nil || editPart.Content == nil {
		// CopyFrom may have copied a blob sealed under the edit's own ID; it's useless, drop it.
		meta.ForkSealed = ""
		return
	}
	history := prev.EditHistory
	if prev.Content != nil && prev.Content.Body != editPart.Content.Body {
		history = append([]string{prev.Content.Body}, prev.EditHistory...)
		if len(history) > maxEditHistory {
			history = history[:maxEditHistory]
		}
	}
	meta.ForkSealed = waid.ForkSeal(waid.ForkWAMessageID(part.ID), &waid.ForkStored{Content: editPart.Content, EditHistory: history})
	if wa.loginMeta().ShowEditsEnabled() && len(history) > 0 {
		editPart.Content = withEditHistory(editPart.Content, history)
	}
	msgconv.ForkCapContent(context.Background(), editPart.Content, editPart.Extra, msgconv.ForkMaxContentBytes/2)
}

const maxEditHistory = 10

// withEditHistory returns a copy of content with previous versions appended, newest first.
func withEditHistory(orig *event.MessageEventContent, history []string) *event.MessageEventContent {
	c := *orig
	formatted := c.FormattedBody
	if c.Format != event.FormatHTML || formatted == "" {
		formatted = strings.ReplaceAll(html.EscapeString(c.Body), "\n", "<br/>")
	}
	var plain, rich strings.Builder
	plain.WriteString(c.Body)
	rich.WriteString(formatted)
	for _, old := range history {
		plain.WriteString("\n✏️ was: " + old)
		rich.WriteString(`<br/><font color="#888888"><sub>✏️ was: ` +
			strings.ReplaceAll(html.EscapeString(old), "\n", "<br/>") + "</sub></font>")
	}
	c.Body = plain.String()
	c.Format = event.FormatHTML
	c.FormattedBody = rich.String()
	return &c
}

var cmdShowEdits = settingCommand("show-edits",
	"Show previous versions under edited messages",
	"When on, edited messages list their earlier versions underneath (\"✏️ was: …\"), newest first. "+
		"Beeper otherwise only shows the latest version.",
	func(m *waid.UserLoginMetadata) bool { return m.ShowEditsEnabled() },
	func(m *waid.UserLoginMetadata, v bool) { m.ShowEdits = &v })
