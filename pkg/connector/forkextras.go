package connector

// Fork additions (weeebdev/matrix-whatsapp): on-demand voice transcription and TikTok link
// downloads. Kept in one file so rebasing onto upstream stays easy.

import (
	"bytes"
	"context"
	"encoding/json"
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
				ReplyTo: &networkid.MessageOptionalPartID{MessageID: target.ID, PartID: &target.PartID},
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
	ctx, cancel := context.WithTimeout(log.WithContext(context.Background()), 3*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "tiktok-")
	if err != nil {
		log.Err(err).Msg("Failed to create temp dir")
		return
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--no-playlist", "--no-progress", "--quiet", "--no-warnings",
		"--max-filesize", "100M",
		"-f", "b[ext=mp4][vcodec^=h264]/b[ext=mp4]/b",
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"),
		"--print", "after_move:filepath",
		link)
	out, err := cmd.Output()
	if err != nil {
		log.Warn().Err(err).Msg("TikTok download failed")
		return
	}
	path := strings.TrimSpace(string(out))
	stat, err := os.Stat(path)
	if err != nil || stat.Size() == 0 || stat.Size() > forkTikTokMaxBytes {
		log.Warn().Err(err).Msg("TikTok download missing or too large")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Err(err).Msg("Failed to read downloaded video")
		return
	}
	log.Info().Int("bytes", len(data)).Msg("Downloaded TikTok video")
	targetID := info.GetID()
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
