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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush/collect"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/notify"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

const aiCutoffPercent = 50.0

func aiPausedPath() string { return filepath.Join(filepath.Dir(people.Root()), "ai-paused") }

// An explicit owner resume is bounded; the original pause file stays intact.
type aiResumeGrant struct {
	AuthorizedAt time.Time `json:"authorizedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Reason       string    `json:"reason"`
}

func aiResumeActive(now time.Time) bool {
	// The owner's go-ahead holds even if the lease lapsed; once it has ended
	// (the next crossing), its lease no longer counts either.
	if _, ok := aiGoAheadActive(now); ok {
		return true
	}
	if aiGoAheadEnded(now) {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(aiPausedPath()), "ai-resume.json"))
	if err != nil {
		return false
	}
	var grant aiResumeGrant
	if json.Unmarshal(raw, &grant) != nil || grant.Reason == "" || grant.AuthorizedAt.After(now) || !grant.ExpiresAt.After(now) || grant.ExpiresAt.Sub(grant.AuthorizedAt) > 4*time.Hour {
		return false
	}
	if info, err := os.Stat(aiPausedPath()); err == nil && info.ModTime().After(grant.AuthorizedAt) {
		return false
	}
	return true
}

// aiPaused says why AI work is paused, or "".
func aiPaused() string {
	if aiResumeActive(time.Now()) {
		return ""
	}
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
	pct, _, ok := codexWeeklyWindow(now)
	return pct, ok
}

// currentWeekly is a weekly reading only while its window is open: after the
// reset the old percent describes a finished week (2 Nov: 67% read on).
func currentWeekly(w *devicepush.OfficialWindow, now time.Time) (float64, time.Time, bool) {
	if w == nil {
		return 0, time.Time{}, false
	}
	var reset time.Time
	if w.ResetsAt != nil {
		reset, _ = time.Parse(time.RFC3339Nano, *w.ResetsAt)
	}
	if !reset.IsZero() && !reset.After(now) {
		return 0, time.Time{}, false
	}
	return w.UsedPercent, reset, true
}

// codexSeatFor is the newest Codex reading of the owner or any board seat.
func codexSeatFor(home string, now time.Time) *devicepush.SeatUsage {
	return collect.CodexSeat([]string{filepath.Join(home, ".codex", "sessions")},
		collect.PaperclipCodexHomes(filepath.Join(home, ".paperclip")), now, codexCache, collect.SeatInfo{ID: "guard"})
}

type agentPauser interface {
	Agents(ctx context.Context) ([]paperclip.Agent, error)
	PauseAgent(ctx context.Context, agentID string) (bool, error)
}

// aiGuardPass pauses the board at the cutoff; it returns the event to send
// when it just paused.
func aiGuardPass(ctx context.Context, b agentPauser, now time.Time) []notify.Event {
	// The owner's one go-ahead (ai_control.go) keeps the short lease renewed until the reset.
	if g, ok := aiGoAheadActive(now); ok {
		_ = renewAiLease(now, g)
	}
	if aiResumeActive(now) {
		return nil
	}
	// While paused, keep every agent paused whatever the meter says: a stop
	// that failed (Pause now, a board timeout) is retried here.
	paused := aiPaused() != ""
	pct, ok := codexWeekly(now)
	if !paused && (!ok || pct < aiCutoffPercent) {
		return nil
	}
	// The owner may resume while this pass waits on the board: recheck before
	// each change so a stale pass never overrides the go-ahead.
	resumed := func() bool { return aiResumeActive(time.Now()) }
	agents, err := b.Agents(ctx)
	if err == nil {
		for _, a := range agents {
			if resumed() {
				return nil
			}
			if a.Status != "paused" {
				_, _ = b.PauseAgent(ctx, a.ID)
			}
		}
	}
	if paused {
		return nil
	}
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	if resumed() || aiPaused() != "" {
		return nil
	}
	why := fmt.Sprintf("Codex weekly usage reached %.0f%% at %s; all agents paused until you say resume.", pct, now.Format("Mon 15:04"))
	_ = os.WriteFile(aiPausedPath(), []byte(why+"\n"), 0o600)
	return []notify.Event{{ID: "ai-paused-" + now.Format("20060102T1504"), Kind: notify.KindHealth,
		Title: "AI work paused at 50%", Body: why, URL: "/os/usage"}}
}

// The provider's rolling window is distinct from the owner's persistent weekly
// cutoff. Do not launch more work into a known exhausted short-term window.
var codexShortTerm = func(now time.Time) (float64, bool) {
	home, _ := os.UserHomeDir()
	seat := collect.CodexSeat([]string{filepath.Join(home, ".codex", "sessions")}, collect.PaperclipCodexHomes(filepath.Join(home, ".paperclip")), now, codexCache, collect.SeatInfo{ID: "guard"})
	if seat == nil || seat.Official == nil || seat.Official.Session == nil {
		return 0, false
	}
	return seat.Official.Session.UsedPercent, true
}
