package waid

// Fork (weeebdev/matrix-whatsapp): encrypted storage of bridged message content for keep-deleted
// and show-edits. Content is stored in message metadata, which ends up in database snapshots and
// backups, so it's sealed with AES-256-GCM using a key from the CONTENT_ENCRYPTION_KEY environment
// variable (base64 of 32 bytes), which never touches the database. Without the key nothing is
// stored. Each blob is bound to its WhatsApp message ID.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

const ForkContentKeyEnv = "CONTENT_ENCRYPTION_KEY"

type ForkStored struct {
	Content     *event.MessageEventContent `json:"c"`
	EditHistory []string                   `json:"h,omitempty"`
}

var (
	forkAEAD     cipher.AEAD
	forkKeyErr   error
	forkLoadOnce sync.Once
)

// ForkLoadKey reads the content key from the environment. It returns an error for a malformed key;
// a missing key isn't an error (storage is just disabled).
func ForkLoadKey() (enabled bool, err error) {
	forkLoadOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv(ForkContentKeyEnv))
		if raw == "" {
			return
		}
		forkAEAD, forkKeyErr = newForkAEAD(raw)
	})
	return forkAEAD != nil, forkKeyErr
}

func newForkAEAD(b64Key string) (cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", ForkContentKeyEnv, err)
	} else if len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes, got %d", ForkContentKeyEnv, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ForkSeal encrypts sc bound to a WhatsApp message ID. Returns "" when storage is disabled.
func ForkSeal(waMsgID types.MessageID, sc *ForkStored) string {
	if enabled, _ := ForkLoadKey(); !enabled || sc == nil || sc.Content == nil {
		return ""
	}
	plain, err := json.Marshal(sc)
	if err != nil {
		return ""
	}
	nonce := make([]byte, forkAEAD.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(forkAEAD.Seal(nonce, nonce, plain, []byte(waMsgID)))
}

// ForkOpen decrypts a blob sealed by ForkSeal for the same WhatsApp message ID.
func ForkOpen(waMsgID types.MessageID, sealed string) (*ForkStored, error) {
	if enabled, err := ForkLoadKey(); !enabled {
		if err == nil {
			err = errors.New("content key not configured")
		}
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	ns := forkAEAD.NonceSize()
	if len(data) < ns {
		return nil, errors.New("sealed content too short")
	}
	plain, err := forkAEAD.Open(nil, data[:ns], data[ns:], []byte(waMsgID))
	if err != nil {
		return nil, err
	}
	var sc ForkStored
	return &sc, json.Unmarshal(plain, &sc)
}

// ForkWAMessageID extracts the WhatsApp message ID used as the binding for a bridged message.
func ForkWAMessageID(id networkid.MessageID) types.MessageID {
	if parsed, err := ParseMessageID(id); err == nil {
		return parsed.ID
	}
	return types.MessageID(id)
}
