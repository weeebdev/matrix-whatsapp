package msgconv

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"maunium.net/go/mautrix/event"
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
