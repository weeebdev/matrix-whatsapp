package connector

// Fork additions (weeebdev/matrix-whatsapp): on-demand voice transcription and TikTok link
// downloads. Kept in one file so rebasing onto upstream stays easy.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/variationselector"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

// ---- Transcription: react with 📝 on a voice message ----
//
// The reaction is kept in Matrix but never sent to WhatsApp. The bridge sends the audio to an
// OpenAI-compatible /audio/transcriptions endpoint and replies with the text. Configured with
// env vars TRANSCRIBE_API_URL (base URL, e.g. https://gateway.example/v1), TRANSCRIBE_API_KEY,
// TRANSCRIBE_MODEL and optionally TRANSCRIBE_LANGUAGE (ISO-639-1, empty = auto-detect).

const (
	forkTranscribeEmoji   = "📝"
	forkTranscribeEmojiID = networkid.EmojiID("fork-transcribe")
)

func isForkTranscribeReaction(key string) bool {
	return variationselector.Remove(key) == forkTranscribeEmoji
}

// forkPreHandleReaction claims 📝 reactions so they aren't limited to one per sender and never
// replace a real WhatsApp reaction.
func (wa *WhatsAppClient) forkPreHandleReaction(msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, bool) {
	if !isForkTranscribeReaction(msg.Content.RelatesTo.Key) {
		return bridgev2.MatrixReactionPreResponse{}, false
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID: waid.MakeUserID(wa.GetLID()),
		EmojiID:  forkTranscribeEmojiID,
		Emoji:    forkTranscribeEmoji,
	}, true
}

// forkHandleReaction starts a transcription for 📝 reactions instead of sending them to WhatsApp.
func (wa *WhatsAppClient) forkHandleReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, bool) {
	if msg.PreHandleResp == nil || msg.PreHandleResp.EmojiID != forkTranscribeEmojiID {
		return nil, false
	}
	target := msg.TargetMessage
	portal := msg.Portal
	log := zerolog.Ctx(ctx).With().Str("action", "fork transcribe").Str("target_id", string(target.ID)).Logger()
	go func() {
		bgCtx, cancel := context.WithTimeout(log.WithContext(context.Background()), 3*time.Minute)
		defer cancel()
		text, err := wa.forkTranscribe(bgCtx, portal, target)
		if err != nil {
			log.Warn().Err(err).Msg("Transcription failed")
			text = "📝 Couldn't transcribe: " + err.Error()
		} else {
			log.Info().Int("chars", len(text)).Msg("Transcribed voice message")
			text = "📝 " + text
		}
		wa.forkPostNotice(portal.PortalKey, target, fmt.Sprintf("fork-transcript-%s-%d", target.ID, time.Now().UnixMilli()), text)
	}()
	return &database.Reaction{}, true
}

func (wa *WhatsAppClient) forkTranscribe(ctx context.Context, portal *bridgev2.Portal, target *database.Message) (string, error) {
	apiURL := strings.TrimRight(os.Getenv("TRANSCRIBE_API_URL"), "/")
	apiKey := os.Getenv("TRANSCRIBE_API_KEY")
	model := os.Getenv("TRANSCRIBE_MODEL")
	if apiURL == "" || model == "" {
		return "", fmt.Errorf("transcription isn't configured")
	}
	stored := forkOpenStored(target)
	if stored == nil || stored.Content == nil {
		return "", fmt.Errorf("only voice messages received after the bridge update can be transcribed")
	}
	content := stored.Content
	if content.MsgType != event.MsgAudio {
		return "", fmt.Errorf("that's not a voice or audio message")
	}
	audio, err := wa.Main.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return "", fmt.Errorf("failed to download audio: %w", err)
	}
	fileName := content.FileName
	if fileName == "" {
		fileName = "voice.ogg"
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("model", model)
	_ = form.WriteField("response_format", "json")
	if lang := os.Getenv("TRANSCRIBE_LANGUAGE"); lang != "" {
		_ = form.WriteField("language", lang)
	}
	fw, err := form.CreateFormFile("file", fileName)
	if err != nil {
		return "", err
	}
	_, _ = fw.Write(audio)
	_ = form.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("transcription API returned HTTP %d: %.200s", resp.StatusCode, data)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("invalid transcription response: %w", err)
	}
	if text := strings.TrimSpace(out.Text); text != "" {
		return text, nil
	}
	return "", fmt.Errorf("no speech recognized")
}

// forkPostNotice posts a bridge-bot notice replying to target.
func (wa *WhatsAppClient) forkPostNotice(portalKey networkid.PortalKey, target *database.Message, id, text string) {
	wa.forkPostNoticeTo(portalKey, &networkid.MessageOptionalPartID{MessageID: target.ID, PartID: &target.PartID}, id, text)
}

func (wa *WhatsAppClient) forkPostNoticeTo(portalKey networkid.PortalKey, replyTo *networkid.MessageOptionalPartID, id, text string) {
	wa.UserLogin.QueueRemoteEvent(&simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		Data: text,
		ID:   networkid.MessageID(id),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, text string) (*bridgev2.ConvertedMessage, error) {
			return &bridgev2.ConvertedMessage{
				ReplyTo: replyTo,
				Parts: []*bridgev2.ConvertedMessagePart{{
					Type:    event.EventMessage,
					Content: &event.MessageEventContent{MsgType: event.MsgNotice, Body: text},
				}},
			}, nil
		},
	})
}

// ---- TikTok links: download the video and post it under the message ----

var tiktokLinkRegex = regexp.MustCompile(`https?://(?:www\.|m\.|vm\.|vt\.)?tiktok\.com/[^\s<>"]+`)

const (
	forkTikTokMaxLinks = 3
	forkTikTokMaxBytes = 100 << 20
)

func forkMessageText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if text := msg.GetConversation(); text != "" {
		return text
	}
	return msg.GetExtendedTextMessage().GetText()
}

// forkHandleLinks downloads TikTok videos linked in a message, in the background.
func (wa *WhatsAppClient) forkHandleLinks(evt *events.Message, msg *waE2E.Message) {
	links := tiktokLinkRegex.FindAllString(forkMessageText(msg), forkTikTokMaxLinks)
	if len(links) == 0 {
		return
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		wa.UserLogin.Log.Debug().Msg("yt-dlp not installed, not downloading TikTok links")
		return
	}
	info := &MessageInfoWrapper{Info: evt.Info, wa: wa}
	for i, link := range links {
		go wa.forkDownloadTikTok(info, i, link)
	}
}

func (wa *WhatsAppClient) forkDownloadTikTok(info *MessageInfoWrapper, index int, link string) {
	log := wa.UserLogin.Log.With().Str("action", "fork tiktok").Str("message_id", info.Info.ID).Logger()
	ctx, cancel := context.WithTimeout(log.WithContext(context.Background()), 15*time.Minute)
	defer cancel()
	targetID := info.GetID()
	data, tooLarge, err := forkFetchTikTok(ctx, link)
	if tooLarge > 0 {
		log.Info().Int64("bytes", tooLarge).Msg("TikTok video too large to bridge")
		wa.forkPostNoticeTo(info.GetPortalKey(), &networkid.MessageOptionalPartID{MessageID: targetID},
			fmt.Sprintf("fork-tiktok-%s-%d", targetID, index),
			fmt.Sprintf("🎬 TikTok video too large to bridge (%d MB)", (tooLarge+(1<<20)-1)>>20))
		return
	} else if err != nil {
		log.Warn().Err(err).Msg("TikTok download failed")
		return
	}
	log.Info().Int("bytes", len(data)).Msg("Downloaded TikTok video")
	wa.UserLogin.QueueRemoteEvent(&simplevent.Message[[]byte]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: info.GetPortalKey(),
			Sender:    info.GetSender(),
			Timestamp: time.Now(),
		},
		Data: data,
		ID:   networkid.MessageID(fmt.Sprintf("fork-tiktok-%s-%d", targetID, index)),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data []byte) (*bridgev2.ConvertedMessage, error) {
			content := &event.MessageEventContent{
				MsgType:  event.MsgVideo,
				Body:     "tiktok.mp4",
				FileName: "tiktok.mp4",
				Info:     &event.FileInfo{MimeType: "video/mp4", Size: len(data)},
			}
			var err error
			content.URL, content.File, err = intent.UploadMedia(ctx, portal.MXID, data, content.FileName, "video/mp4")
			if err != nil {
				return nil, err
			}
			return &bridgev2.ConvertedMessage{
				ReplyTo: &networkid.MessageOptionalPartID{MessageID: targetID},
				Parts:   []*bridgev2.ConvertedMessagePart{{Type: event.EventMessage, Content: content}},
			}, nil
		},
	})
}

type ytFormat struct {
	ID             string  `json:"format_id"`
	VCodec         string  `json:"vcodec"`
	ACodec         string  `json:"acodec"`
	Height         int     `json:"height"`
	FileSize       int64   `json:"filesize"`
	FileSizeApprox float64 `json:"filesize_approx"`
}

func (f *ytFormat) size() int64 {
	if f.FileSize > 0 {
		return f.FileSize
	}
	return int64(f.FileSizeApprox)
}

func (f *ytFormat) hasVideo() bool { return f.VCodec != "" && f.VCodec != "none" }
func (f *ytFormat) hasAudio() bool { return f.ACodec != "" && f.ACodec != "none" }
func (f *ytFormat) isH264() bool {
	return strings.HasPrefix(f.VCodec, "h264") || strings.HasPrefix(f.VCodec, "avc")
}

// tiktokPlan is how to get a video under the size limit: a yt-dlp format selector and whether
// the result needs converting to H.264. tooLarge > 0 means nothing fits (smallest known size).
type tiktokPlan struct {
	selector  string
	transcode bool
	tooLarge  int64
}

// planTikTokDownload picks, in order: H.264 with audio under the limit (largest), else the
// largest video under the limit with any codec (merged with an audio-only format if needed) to be
// converted to H.264, else reports the smallest size as too large.
func planTikTokDownload(formats []ytFormat, limit int64) tiktokPlan {
	var bestH264, bestAny *ytFormat
	var audio *ytFormat
	var smallest int64
	for i := range formats {
		f := &formats[i]
		if !f.hasVideo() && f.hasAudio() && (audio == nil || f.size() < audio.size()) {
			audio = f
		}
		if !f.hasVideo() || f.size() <= 0 || f.Height > 1920 {
			continue
		}
		if smallest == 0 || f.size() < smallest {
			smallest = f.size()
		}
		if f.size() > limit {
			continue
		}
		if f.isH264() && f.hasAudio() && (bestH264 == nil || f.Height > bestH264.Height) {
			bestH264 = f
		}
		if (f.hasAudio() || audio != nil) && (bestAny == nil || f.Height > bestAny.Height || (f.Height == bestAny.Height && f.hasAudio() && !bestAny.hasAudio())) {
			bestAny = f
		}
	}
	switch {
	case bestH264 != nil:
		return tiktokPlan{selector: bestH264.ID}
	case bestAny != nil && bestAny.hasAudio():
		return tiktokPlan{selector: bestAny.ID, transcode: !bestAny.isH264()}
	case bestAny != nil && audio != nil:
		return tiktokPlan{selector: bestAny.ID + "+" + audio.ID, transcode: !bestAny.isH264()}
	case smallest > 0:
		return tiktokPlan{tooLarge: smallest}
	}
	return tiktokPlan{}
}

// forkFetchTikTok returns the video bytes, or tooLarge > 0 if no version fits the size limit.
func forkFetchTikTok(ctx context.Context, link string) (data []byte, tooLarge int64, err error) {
	probe, err := exec.CommandContext(ctx, "yt-dlp", "--no-playlist", "--no-warnings", "-J", link).Output()
	if err != nil {
		return nil, 0, fmt.Errorf("yt-dlp probe failed: %w%s", err, stderrTail(err))
	}
	var meta struct {
		Formats  []ytFormat `json:"formats"`
		Duration float64    `json:"duration"`
	}
	if err = json.Unmarshal(probe, &meta); err != nil {
		return nil, 0, fmt.Errorf("invalid yt-dlp metadata: %w", err)
	}
	plan := planTikTokDownload(meta.Formats, forkTikTokMaxBytes)
	if plan.tooLarge > 0 {
		return nil, plan.tooLarge, nil
	} else if plan.selector == "" {
		return nil, 0, fmt.Errorf("no downloadable video format")
	}
	dir, err := os.MkdirTemp("", "tiktok-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(dir)
	dl := exec.CommandContext(ctx, "yt-dlp", "--no-playlist", "--no-progress", "--no-warnings",
		"-f", plan.selector, "--merge-output-format", "mp4",
		"-o", filepath.Join(dir, "video.%(ext)s"), link)
	if out, err := dl.CombinedOutput(); err != nil {
		return nil, 0, fmt.Errorf("yt-dlp download failed: %w: %s", err, lastLines(out, 5))
	}
	path, err := onlyFile(dir)
	if err != nil {
		return nil, 0, err
	}
	if plan.transcode {
		// Aim the bitrate at the size limit so long videos still fit.
		videoKbps := tiktokTargetVideoKbps(meta.Duration)
		if videoKbps == 0 {
			stat, _ := os.Stat(path)
			return nil, max(stat.Size(), int64(forkTikTokMaxBytes)+1), nil
		}
		converted := filepath.Join(dir, "h264.mp4")
		ff := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", path,
			"-vf", "scale='min(1280,iw)':-2", "-c:v", "libx264", "-preset", "veryfast",
			"-b:v", fmt.Sprintf("%dk", videoKbps), "-maxrate", fmt.Sprintf("%dk", videoKbps*3/2), "-bufsize", fmt.Sprintf("%dk", videoKbps*2),
			"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", converted)
		if out, err := ff.CombinedOutput(); err != nil {
			return nil, 0, fmt.Errorf("ffmpeg conversion failed: %w: %s", err, lastLines(out, 5))
		}
		path = converted
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	} else if stat.Size() > forkTikTokMaxBytes {
		return nil, stat.Size(), nil
	}
	data, err = os.ReadFile(path)
	return data, 0, err
}

// tiktokTargetVideoKbps returns a video bitrate that keeps the converted file under about 90% of
// the size limit with 128 kbps audio, capped at 2500 kbps. 0 means it can't fit at a watchable
// quality (under 300 kbps) or the duration is unknown.
func tiktokTargetVideoKbps(durationSec float64) int {
	if durationSec <= 0 {
		return 0
	}
	totalKbps := float64(forkTikTokMaxBytes) * 0.9 * 8 / 1000 / durationSec
	videoKbps := int(totalKbps) - 128
	if videoKbps > 2500 {
		videoKbps = 2500
	}
	if videoKbps < 300 {
		return 0
	}
	return videoKbps
}

func onlyFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".part") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("yt-dlp produced no file")
}

func lastLines(out []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func stderrTail(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return ": " + lastLines(exitErr.Stderr, 3)
	}
	return ""
}
