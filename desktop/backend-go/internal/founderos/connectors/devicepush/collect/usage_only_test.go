package collect

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush"
)

func TestUsageOnlyNeverReadsOtherSourcesOrUploadsMessages(t *testing.T) {
	home := t.TempDir()
	at := time.Now()
	cfg := DefaultConfig(envResolver(t), home, "Windows-Laptop")
	cfg.UsageOnly = true
	probedOtherSource := false
	cfg.FullDiskAccess = func() bool { probedOtherSource = true; return false }
	write(t, filepath.Join(cfg.ClaudeProjectsDir, "p", "s.jsonl"), `{"type":"assistant","timestamp":"`+at.UTC().Format(time.RFC3339)+`","message":{"content":"private-message-never-upload","model":"claude-test","usage":{"input_tokens":100,"output_tokens":20}}}`+"\n")
	write(t, filepath.Join(cfg.ObsidianVault, "secret.md"), "private-note-never-upload")
	p := NewCollector(cfg).Collect(context.Background(), at)
	if probedOtherSource {
		t.Fatal("usage-only collection probed non-usage access")
	}
	if err := devicepush.Validate(p); err != nil {
		t.Fatal(err)
	}
	if len(p.Statuses) != 0 || p.WhatsApp != nil || p.Wispr != nil || p.Obsidian != nil || p.Ollama != nil {
		t.Fatal("usage-only payload included a non-usage source")
	}
	if len(p.Usage) == 0 || p.Usage[0].ByModel["claude-test"].In != 100 {
		t.Fatal("reporter lost token totals")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "private-message-never-upload") || strings.Contains(string(raw), "private-note-never-upload") {
		t.Fatal("private contents escaped into the upload")
	}
}
