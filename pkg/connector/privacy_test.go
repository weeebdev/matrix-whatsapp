package connector

import (
	"context"
	"os"
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

func TestPlanTikTokDownload(t *testing.T) {
	const mb = 1 << 20
	limit := int64(forkTikTokMaxBytes)
	// Short video: H.264 with audio fits.
	short := []ytFormat{
		{ID: "audio", VCodec: "none", ACodec: "mp3"},
		{ID: "download", VCodec: "h264", ACodec: "aac"},
		{ID: "h264_540p", VCodec: "h264", ACodec: "aac", Height: 1024, FileSize: 7 * mb},
		{ID: "bytevc1_720p", VCodec: "h265", ACodec: "aac", Height: 1280, FileSize: 7 * mb},
	}
	if p := planTikTokDownload(short, limit); p.selector != "h264_540p" || p.transcode {
		t.Errorf("short: %+v", p)
	}
	// Long video: H.264 too big, H.265 video-only fits -> merge audio and transcode.
	long := []ytFormat{
		{ID: "audio", VCodec: "none", ACodec: "mp3"},
		{ID: "bytevc1_540p", VCodec: "h265", ACodec: "none", Height: 1024, FileSize: 49 * mb},
		{ID: "h264_720p", VCodec: "h264", ACodec: "aac", Height: 1280, FileSize: 131 * mb},
		{ID: "bytevc1_720p", VCodec: "h265", ACodec: "none", Height: 1280, FileSize: 58 * mb},
		{ID: "bytevc1_1080p", VCodec: "h265", ACodec: "none", Height: 1920, FileSize: 104 * mb},
	}
	if p := planTikTokDownload(long, limit); p.selector != "bytevc1_720p+audio" || !p.transcode {
		t.Errorf("long: %+v", p)
	}
	// Nothing fits.
	huge := []ytFormat{{ID: "h264_720p", VCodec: "h264", ACodec: "aac", Height: 1280, FileSize: 131 * mb}}
	if p := planTikTokDownload(huge, limit); p.tooLarge != 131*mb || p.selector != "" {
		t.Errorf("huge: %+v", p)
	}
}

// TestFetchTikTokLive downloads a real video. Run with TIKTOK_TEST_URL set and yt-dlp/ffmpeg on PATH.
func TestFetchTikTokLive(t *testing.T) {
	link := os.Getenv("TIKTOK_TEST_URL")
	if link == "" {
		t.Skip("TIKTOK_TEST_URL not set")
	}
	data, tooLarge, err := forkFetchTikTok(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bytes=%d tooLarge=%d", len(data), tooLarge)
	if tooLarge == 0 && len(data) < 1000 {
		t.Fatal("no video data")
	}
	if out := os.Getenv("TIKTOK_TEST_OUT"); out != "" && len(data) > 0 {
		_ = os.WriteFile(out, data, 0o644)
	}
}

func TestTikTokTargetBitrate(t *testing.T) {
	if k := tiktokTargetVideoKbps(474); k < 1300 || k > 1500 {
		t.Errorf("8 min video: %d kbps", k)
	}
	if k := tiktokTargetVideoKbps(30); k != 2500 {
		t.Errorf("short video should be capped: %d", k)
	}
	if k := tiktokTargetVideoKbps(3 * 3600); k != 0 {
		t.Errorf("3 h video should not fit: %d", k)
	}
}
