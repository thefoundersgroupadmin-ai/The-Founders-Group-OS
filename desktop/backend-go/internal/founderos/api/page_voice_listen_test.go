package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWhisperNoiseIsNotSpeech(t *testing.T) {
	for in, want := range map[string]string{
		" How many calls do I have today?\n": "How many calls do I have today?",
		"[BLANK_AUDIO]\n":                    "",
		" (silence) [Music] ok then ":        "ok then",
	} {
		if got := cleanTranscript(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestListenHandsBackTheText(t *testing.T) {
	peopleSetup(t)
	var got []byte
	old := transcribe
	transcribe = func(_ context.Context, audio []byte) (string, error) { got = audio; return "status update please", nil }
	defer func() { transcribe = old }()
	r := asUser(t, &Deps{}, karim)
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/voice/listen", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("empty recording: %d", w.Code)
	}
	w := do(t, r, http.MethodPost, "/api/founderos/pages/voice/listen", "OggS-fake-audio")
	var body struct{ Text string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Text != "status update please" || string(got) != "OggS-fake-audio" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// The real thing, when this Mac has it: a spoken sentence comes back as text.
func TestListenOnThisMac(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if _, err := os.Stat(whisperModel()); err != nil || whisperBin() == "" || ffmpegBin() == "" {
		t.Skip("no whisper on this machine")
	}
	say, err := exec.LookPath("say")
	if err != nil {
		t.Skip("no say")
	}
	aiff := filepath.Join(t.TempDir(), "q.aiff")
	if err := exec.Command(say, "-o", aiff, "How many calls do I have today").Run(); err != nil {
		t.Skip("say failed")
	}
	audio, _ := os.ReadFile(aiff)
	text, err := transcribe(context.Background(), audio)
	if err != nil || text != "How many calls do I have today?" {
		t.Fatalf("%q %v", text, err)
	}
}
