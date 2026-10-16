package msgconv

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestForkCapContent(t *testing.T) {
	ctx := context.Background()
	small := &event.MessageEventContent{MsgType: event.MsgText, Body: "hi", Format: event.FormatHTML, FormattedBody: "<b>hi</b>"}
	if ForkCapContent(ctx, small, nil, ForkMaxContentBytes) || small.FormattedBody == "" {
		t.Fatal("small content must not change")
	}

	// Long Cyrillic text (2 bytes per rune) with HTML: formatting dropped, body cut on a rune boundary.
	long := strings.Repeat("Длинное сообщение ", 6000)
	c := &event.MessageEventContent{MsgType: event.MsgText, Body: long, Format: event.FormatHTML, FormattedBody: "<p>" + long + "</p>"}
	if !ForkCapContent(ctx, c, nil, ForkMaxContentBytes) {
		t.Fatal("expected change")
	}
	if n := forkContentSize(c, nil); n > ForkMaxContentBytes {
		t.Fatalf("still %d bytes", n)
	}
	if c.FormattedBody != "" || !strings.HasSuffix(c.Body, forkTruncatedMarker) || !utf8.ValidString(c.Body) {
		t.Fatalf("bad truncation: html=%d valid=%v", len(c.FormattedBody), utf8.ValidString(c.Body))
	}
	if len(c.Body) < ForkMaxContentBytes/2 {
		t.Errorf("truncated too aggressively: %d bytes left", len(c.Body))
	}

	// Short body but a huge extra (e.g. link preview): extra dropped, body kept.
	extra := map[string]any{"com.beeper.linkpreviews": []any{map[string]any{"og:description": strings.Repeat("x", 80_000)}}, "small": 1}
	c2 := &event.MessageEventContent{MsgType: event.MsgText, Body: "see link"}
	ForkCapContent(ctx, c2, extra, ForkMaxContentBytes)
	if _, ok := extra["com.beeper.linkpreviews"]; ok || c2.Body != "see link" {
		t.Fatalf("expected link preview dropped, body kept: %q %v", c2.Body, extra)
	}
	if forkContentSize(c2, extra) > ForkMaxContentBytes {
		t.Fatal("still too big")
	}
}

type stubIntent struct {
	bridgev2.MatrixAPI
	uploaded []byte
}

func (s *stubIntent) UploadMedia(ctx context.Context, roomID id.RoomID, data []byte, fileName, mimeType string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	s.uploaded = data
	return "mxc://test/fulltext", nil, nil
}

func TestForkCapConvertedAttachesFullText(t *testing.T) {
	long := strings.Repeat("строка текста\n", 6000)
	newCM := func() *bridgev2.ConvertedMessage {
		return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: &event.MessageEventContent{MsgType: event.MsgText, Body: long},
		}}}
	}

	// No intent in context (e.g. tests, unusual paths): plain truncation, no attachment.
	cm := newCM()
	forkCapConverted(context.Background(), cm)
	if len(cm.Parts) != 1 || !strings.HasSuffix(cm.Parts[0].Content.Body, forkTruncatedMarker) {
		t.Fatalf("expected plain truncation, got %d parts", len(cm.Parts))
	}

	// With an intent: full text attached as message.txt, note in the truncated body.
	stub := &stubIntent{}
	ctx := context.WithValue(context.Background(), contextKeyIntent, bridgev2.MatrixAPI(stub))
	ctx = context.WithValue(ctx, contextKeyPortal, &bridgev2.Portal{Portal: &database.Portal{MXID: "!room:test"}})
	cm = newCM()
	forkCapConverted(ctx, cm)
	if len(cm.Parts) != 2 {
		t.Fatalf("expected text + attachment, got %d parts", len(cm.Parts))
	}
	if !strings.HasSuffix(cm.Parts[0].Content.Body, forkFullTextNote) {
		t.Errorf("truncated body should point to the attachment")
	}
	if att := cm.Parts[1]; att.Content.MsgType != event.MsgFile || att.Content.FileName != "message.txt" || att.Content.URL != "mxc://test/fulltext" {
		t.Errorf("bad attachment: %+v", att.Content)
	}
	if string(stub.uploaded) != long {
		t.Errorf("attachment must hold the complete original text (%d of %d bytes)", len(stub.uploaded), len(long))
	}
	if forkContentSize(cm.Parts[0].Content, cm.Parts[0].Extra) > ForkMaxContentBytes {
		t.Error("text part still too big")
	}
}
