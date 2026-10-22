package api

// POST /pages/voice/listen turns a short recording into text on this Mac
// (ffmpeg → whisper.cpp), so talking to the OS works where the browser has no
// speech recognition of its own: the desktop app (Electron needs Google's
// key for it), the iPhone home-screen app. Audio never leaves the machine.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// listenMax is about two minutes of opus; a turn in a conversation is far less.
const listenMax = 8 << 20

var (
	whisperBin = func() string {
		if p := os.Getenv("FOUNDEROS_WHISPER_BIN"); p != "" {
			return p
		}
		for _, p := range []string{"/opt/homebrew/bin/whisper-cli", "/usr/local/bin/whisper-cli"} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		p, _ := exec.LookPath("whisper-cli")
		return p
	}
	whisperModel = func() string {
		if p := os.Getenv("FOUNDEROS_WHISPER_MODEL"); p != "" {
			return p
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".founderos", "models", "ggml-base.en.bin")
	}
	ffmpegBin = func() string {
		for _, p := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg"} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		p, _ := exec.LookPath("ffmpeg")
		return p
	}
)

// whisper's markers for no speech: "[BLANK_AUDIO]", "(silence)", "[Music]".
var whisperNoise = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)

func cleanTranscript(out string) string {
	t := whisperNoise.ReplaceAllString(out, " ")
	return strings.Join(strings.Fields(t), " ")
}

var transcribe = func(ctx context.Context, audio []byte) (string, error) {
	bin, model, ff := whisperBin(), whisperModel(), ffmpegBin()
	if bin == "" || ff == "" {
		return "", errors.New("speech-to-text is not installed on the OS Mac (whisper-cpp, ffmpeg)")
	}
	if _, err := os.Stat(model); err != nil {
		return "", errors.New("the speech model is missing on the OS Mac")
	}
	dir, err := os.MkdirTemp("", "tfg-listen-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	in, wav := filepath.Join(dir, "in"), filepath.Join(dir, "in.wav")
	if err := os.WriteFile(in, audio, 0o600); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, ff, "-loglevel", "error", "-y", "-i", in, "-ar", "16000", "-ac", "1", wav).CombinedOutput(); err != nil {
		return "", errors.New("could not read that recording: " + strings.TrimSpace(string(out)))
	}
	out, err := exec.CommandContext(ctx, bin, "-m", model, "-f", wav, "-np", "-nt", "-l", "en").Output()
	if err != nil {
		return "", errors.New("transcription failed")
	}
	return cleanTranscript(string(out)), nil
}

func registerVoiceListen(s gin.IRoutes) {
	s.POST("/pages/voice/listen", func(c *gin.Context) {
		audio, err := io.ReadAll(io.LimitReader(c.Request.Body, listenMax+1))
		if err != nil || len(audio) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no recording"})
			return
		}
		if len(audio) > listenMax {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "that recording is too long"})
			return
		}
		text, err := transcribe(c.Request.Context(), audio)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"text": text})
	})
}
