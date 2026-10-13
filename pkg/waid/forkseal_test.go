package waid

import (
	"encoding/base64"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestForkSealRoundTrip(t *testing.T) {
	t.Setenv(ForkContentKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	forkLoadOnce, forkAEAD, forkKeyErr = sync.Once{}, nil, nil
	sealed := ForkSeal("MSG1", &ForkStored{Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "секрет"}, EditHistory: []string{"old"}})
	if sealed == "" || strings.Contains(sealed, "секрет") {
		t.Fatalf("bad seal %q", sealed)
	}
	out, err := ForkOpen("MSG1", sealed)
	if err != nil || out.Content.Body != "секрет" || out.EditHistory[0] != "old" {
		t.Fatalf("round trip: %v %+v", err, out)
	}
	if _, err = ForkOpen("MSG2", sealed); err == nil {
		t.Fatal("blob must not open under another message ID")
	}
	merged := &MessageMetadata{ForkSealed: sealed}
	merged.CopyFrom(&MessageMetadata{})
	if merged.ForkSealed != sealed {
		t.Fatal("CopyFrom without content must keep the stored blob")
	}
}

func TestForkSealDisabledWithoutKey(t *testing.T) {
	t.Setenv(ForkContentKeyEnv, "")
	forkLoadOnce, forkAEAD, forkKeyErr = sync.Once{}, nil, nil
	if ForkSeal("MSG1", &ForkStored{Content: &event.MessageEventContent{Body: "x"}}) != "" {
		t.Fatal("nothing should be stored without a key")
	}
	t.Setenv(ForkContentKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 16)))
	forkLoadOnce, forkAEAD, forkKeyErr = sync.Once{}, nil, nil
	if _, err := ForkLoadKey(); err == nil {
		t.Fatal("16-byte key must be rejected")
	}
}
