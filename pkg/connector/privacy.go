package connector

// Fork additions (weeebdev/mautrix-whatsapp): per-login "invisible" mode and keeping messages
// that are deleted for everyone. Kept in one file so rebasing onto upstream stays easy.

import (
	"context"
	"fmt"
	"html"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

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

// convertRevokeToDeletedEdit re-renders the existing Matrix events of a revoked message as
// struck-through text, or keeps media with a marked caption.
func (evt *WAMessageEvent) convertRevokeToDeletedEdit(ctx context.Context, portal *bridgev2.Portal, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	edit := &bridgev2.ConvertedEdit{}
	for _, part := range existing {
		orig, err := evt.wa.Main.Bridge.Bot.GetEvent(ctx, portal.MXID, part.MXID)
		if err != nil || orig == nil {
			continue
		}
		content := orig.Content.AsMessage()
		if content == nil || content.MsgType == "" || strings.Contains(content.Body, deletedLabel) {
			continue
		}
		if content.NewContent != nil {
			content = content.NewContent
		}
		edit.ModifiedParts = append(edit.ModifiedParts, &bridgev2.ConvertedEditPart{
			Part:    part,
			Type:    event.EventMessage,
			Content: deletedContent(content),
		})
	}
	if len(edit.ModifiedParts) == 0 {
		return nil, fmt.Errorf("%w: nothing to mark as deleted", bridgev2.ErrIgnoringRemoteEvent)
	}
	return edit, nil
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
