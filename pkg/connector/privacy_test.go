package connector

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func TestDeletedContent(t *testing.T) {
	text := deletedContent(&event.MessageEventContent{MsgType: event.MsgText, Body: "a < b\nc"})
	if text.FormattedBody != "<del>a &lt; b<br/>c</del><br/><em>"+deletedLabel+"</em>" || text.Body != "~~a < b\nc~~\n"+deletedLabel {
		t.Errorf("text: %q / %q", text.FormattedBody, text.Body)
	}
	orig := &event.MessageEventContent{MsgType: event.MsgImage, Body: "nice pic", FileName: "pic.jpg", URL: "mxc://x/y"}
	img := deletedContent(orig)
	if img.URL != "mxc://x/y" || img.FileName != "pic.jpg" || img.Body != "nice pic\n"+deletedLabel {
		t.Errorf("image: %+v", img)
	}
	if orig.Body != "nice pic" {
		t.Error("original content must not be modified")
	}
}

func TestSettings(t *testing.T) {
	var meta waid.UserLoginMetadata
	if !meta.KeepDeletedEnabled() || meta.Invisible {
		t.Fatal("defaults should be keep-deleted on, invisible off")
	}
	for in, want := range map[string]bool{"on": true, "OFF": false} {
		if v, ok := boolArg([]string{in}); !ok || v != want {
			t.Errorf("boolArg(%q) = %v, %v", in, v, ok)
		}
	}
}

func TestWithEditHistory(t *testing.T) {
	c := withEditHistory(&event.MessageEventContent{MsgType: event.MsgText, Body: "v3"}, []string{"v2", "a<b"})
	if c.Body != "v3\n✏️ was: v2\n✏️ was: a<b" {
		t.Errorf("body %q", c.Body)
	}
	want := `v3<br/><font color="#888888"><sub>✏️ was: v2</sub></font><br/><font color="#888888"><sub>✏️ was: a&lt;b</sub></font>`
	if c.FormattedBody != want {
		t.Errorf("html %q", c.FormattedBody)
	}
}

func TestTikTokLinks(t *testing.T) {
	text := "look https://vm.tiktok.com/ZMabc123/ and https://www.tiktok.com/@user/video/7412345678901234567?is_from_webapp=1 but not https://example.com/tiktok.com"
	got := tiktokLinkRegex.FindAllString(text, forkTikTokMaxLinks)
	if len(got) != 2 || got[0] != "https://vm.tiktok.com/ZMabc123/" || !strings.HasPrefix(got[1], "https://www.tiktok.com/@user/video/") {
		t.Errorf("links %q", got)
	}
	if !isForkTranscribeReaction("📝") || isForkTranscribeReaction("👍") {
		t.Error("transcribe reaction detection")
	}
}
