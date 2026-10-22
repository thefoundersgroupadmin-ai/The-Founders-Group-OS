package api

// The owner's standing rule (AGENTS.md, 2026-10-22): when the shared Codex
// weekly meter reaches 50%, all AI work pauses until the owner says resume. A
// weekly reset does not resume it. This makes that real for the board: every
// pass reads the official meter (the newest Codex session of the owner or any
// board seat), and at or over the cutoff pauses every agent, writes
// ~/.founderos/ai-paused, and tells the phone once. While the file exists the
// OS wakes no agent (the Conductor's hourly retry included). Only the owner
// removes it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush/collect"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/notify"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

const aiCutoffPercent = 50.0

func aiPausedPath() string { return filepath.Join(filepath.Dir(people.Root()), "ai-paused") }

// aiPaused says why AI work is paused, or "".
func aiPaused() string {
	raw, err := os.ReadFile(aiPausedPath())
	if err != nil {
		return ""
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return "paused"
}

var codexCache = collect.NewCodexCache()

// codexWeekly is the official weekly percent used, and whether one was found.
var codexWeekly = func(now time.Time) (float64, bool) {
	home, _ := os.UserHomeDir()
	seat := collect.CodexSeat([]string{filepath.Join(home, ".codex", "sessions")},
		collect.PaperclipCodexHomes(filepath.Join(home, ".paperclip")), now, codexCache, collect.SeatInfo{ID: "guard"})
	if seat == nil || seat.Official == nil || seat.Official.Weekly == nil {
		return 0, false
	}
	return seat.Official.Weekly.UsedPercent, true
}

type agentPauser interface {
	Agents(ctx context.Context) ([]paperclip.Agent, error)
	PauseAgent(ctx context.Context, agentID string) (bool, error)
}

// aiGuardPass pauses the board at the cutoff; it returns the event to send
// when it just paused.
func aiGuardPass(ctx context.Context, b agentPauser, now time.Time) []notify.Event {
	pct, ok := codexWeekly(now)
	if !ok || pct < aiCutoffPercent {
		return nil
	}
	first := aiPaused() == ""
	agents, err := b.Agents(ctx)
	if err == nil {
		for _, a := range agents {
			if a.Status != "paused" {
				_, _ = b.PauseAgent(ctx, a.ID)
			}
		}
	}
	if !first {
		return nil
	}
	why := fmt.Sprintf("Codex weekly usage reached %.0f%% at %s; all agents paused until you say resume.", pct, now.Format("Mon 15:04"))
	_ = os.WriteFile(aiPausedPath(), []byte(why+"\n"), 0o600)
	return []notify.Event{{ID: "ai-paused-" + now.Format("20060102T1504"), Kind: notify.KindHealth,
		Title: "AI work paused at 50%", Body: why, URL: "/os/usage"}}
}
