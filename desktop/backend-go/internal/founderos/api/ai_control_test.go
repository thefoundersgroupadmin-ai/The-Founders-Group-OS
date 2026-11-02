package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
)

type controlBoard struct {
	pauseBoard
	pauses  map[string]time.Time
	resumed []string
}

func (b *controlBoard) ResumeAgent(_ context.Context, id string) (bool, error) {
	b.resumed = append(b.resumed, id)
	return true, nil
}
func (b *controlBoard) AgentPauses(context.Context) (map[string]time.Time, error) {
	return b.pauses, nil
}

func aiControlSetup(t *testing.T, pct float64, reset time.Time) *controlBoard {
	t.Helper()
	peopleSetup(t)
	oldW, oldWin, oldB := codexWeekly, codexWeeklyWindow, aiBoard
	codexWeekly = func(time.Time) (float64, bool) { return pct, true }
	codexWeeklyWindow = func(time.Time) (float64, time.Time, bool) { return pct, reset, true }
	b := &controlBoard{}
	aiBoard = func(*Deps) aiBoardAPI { return b }
	t.Cleanup(func() { codexWeekly, codexWeeklyWindow, aiBoard = oldW, oldWin, oldB })
	return b
}

func TestTheOwnersOneClickResumesUntilTheWeeklyResetAndOnlyWakesGuardPausedAgents(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(5 * 24 * time.Hour).Truncate(time.Second)
	b := aiControlSetup(t, 67, reset)
	pausedAt := now.Add(-26 * time.Hour)
	os.WriteFile(aiPausedPath(), []byte("Codex weekly usage reached 50%"), 0600)
	os.Chtimes(aiPausedPath(), pausedAt, pausedAt)
	b.agents = []paperclip.Agent{{ID: "ceo", Name: "CEO", Status: "paused"}, {ID: "old", Name: "Old", Status: "paused"}, {ID: "pr", Name: "PR", Status: "idle"}}
	b.pauses = map[string]time.Time{"ceo": pausedAt.Add(time.Second), "old": pausedAt.Add(-72 * time.Hour)}
	r := asUser(t, &Deps{}, ownerUser)

	w := do(t, r, http.MethodGet, "/api/founderos/pages/usage/ai", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"paused":true`) || !strings.Contains(w.Body.String(), `"canControl":true`) || !strings.Contains(w.Body.String(), `"usedPercent":67`) {
		t.Fatalf("status before: %d %s", w.Code, w.Body)
	}
	w = do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/resume", "")
	var out struct {
		Resumed []string `json:"resumed"`
		Kept    []string `json:"kept"`
		Until   time.Time
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out.Resumed) != 1 || out.Resumed[0] != "CEO" || len(out.Kept) != 1 || out.Kept[0] != "Old" || !out.Until.Equal(reset) {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	if len(b.resumed) != 1 || b.resumed[0] != "ceo" {
		t.Fatalf("woke %v; only the guard-paused agent may wake", b.resumed)
	}
	if aiPaused() != "" || !aiResumeActive(time.Now()) {
		t.Fatal("AI work still paused after the owner's go-ahead")
	}
	// The old flag is kept as a backup, not deleted.
	if m, _ := filepath.Glob(aiPausedPath() + ".cleared-*"); len(m) != 1 {
		t.Fatalf("backup of the pause flag: %v", m)
	}

	// Days later, still above 50%: the guard renews the short lease and pauses nothing.
	later := now.Add(3 * 24 * time.Hour)
	b.agents = []paperclip.Agent{{ID: "ceo", Status: "running"}}
	b.paused = nil
	if evs := aiGuardPass(context.Background(), b, later); len(evs) != 0 || len(b.paused) != 0 || !aiResumeActive(later) {
		t.Fatalf("the go-ahead must hold until the reset: %v paused=%v", evs, b.paused)
	}
	// After the reset, crossing 50% again pauses as before.
	after := reset.Add(time.Hour)
	if evs := aiGuardPass(context.Background(), b, after); len(evs) != 1 || len(b.paused) != 1 || aiPaused() == "" {
		t.Fatalf("after the reset the guard must pause again: %v paused=%v", evs, b.paused)
	}
}

func TestPauseNowStopsEverythingAndEndsTheGoAhead(t *testing.T) {
	now := time.Now().UTC()
	b := aiControlSetup(t, 67, now.Add(48*time.Hour))
	r := asUser(t, &Deps{}, ownerUser)
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/resume", ""); w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	b.agents = []paperclip.Agent{{ID: "ceo", Status: "running"}, {ID: "x", Status: "paused"}}
	w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/pause", "")
	if w.Code != 200 || len(b.paused) != 1 || b.paused[0] != "ceo" || aiPaused() == "" || aiResumeActive(time.Now()) {
		t.Fatalf("pause: %d %s paused=%v", w.Code, w.Body, b.paused)
	}
	// The guard does not quietly renew a lease after Pause now.
	aiGuardPass(context.Background(), b, now.Add(time.Minute))
	if aiResumeActive(now.Add(time.Minute)) {
		t.Fatal("go-ahead survived Pause now")
	}
}

func TestOnlyTheOwnerCanResumeOrPause(t *testing.T) {
	now := time.Now().UTC()
	b := aiControlSetup(t, 67, now.Add(48*time.Hour))
	os.WriteFile(aiPausedPath(), []byte("cutoff"), 0600)
	r := asUser(t, &Deps{}, karim)
	if w := do(t, r, http.MethodGet, "/api/founderos/pages/usage/ai", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"canControl":false`) {
		t.Fatalf("teammate status: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"resume", "pause"} {
		if w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/"+p, ""); w.Code != http.StatusForbidden {
			t.Fatalf("teammate %s: %d", p, w.Code)
		}
	}
	if aiPaused() == "" || len(b.resumed)+len(b.paused) != 0 {
		t.Fatal("a teammate changed the pause")
	}
}

func TestWithoutAKnownResetTheGoAheadLastsAWeek(t *testing.T) {
	now := time.Now().UTC()
	aiControlSetup(t, 67, time.Time{})
	codexWeeklyWindow = func(time.Time) (float64, time.Time, bool) { return 67, time.Time{}, true }
	w := do(t, asUser(t, &Deps{}, ownerUser), http.MethodPost, "/api/founderos/pages/usage/ai/resume", "")
	var out struct{ Until time.Time }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.Until.Before(now.Add(7*24*time.Hour-time.Minute)) || out.Until.After(now.Add(7*24*time.Hour+time.Minute)) {
		t.Fatalf("fallback: %d %s", w.Code, w.Body)
	}
}

// Pause now pauses agents one by one; a Resume pressed meanwhile (31 Oct: ten
// seconds later) must stop the loop instead of leaving the rest paused.
type resumedMidPause struct{ *controlBoard }

func (b resumedMidPause) PauseAgent(ctx context.Context, id string) (bool, error) {
	ok, err := b.controlBoard.PauseAgent(ctx, id)
	os.Remove(aiPausedPath()) // the owner's Resume clears the pause
	return ok, err
}

func TestResumeDuringPauseNowStopsThePausing(t *testing.T) {
	now := time.Now().UTC()
	b := aiControlSetup(t, 67, now.Add(48*time.Hour))
	b.agents = []paperclip.Agent{{ID: "a", Status: "idle"}, {ID: "b", Status: "idle"}, {ID: "c", Status: "running"}}
	aiBoard = func(*Deps) aiBoardAPI { return resumedMidPause{b} }
	r := asUser(t, &Deps{}, ownerUser)
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/pause", ""); w.Code != 200 {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}
	if len(b.paused) != 1 {
		t.Fatalf("kept pausing after the owner resumed: %v", b.paused)
	}
}

// Agents the last pause caught after the owner resumed are listed on the card
// and woken by one press; agents paused before that pause stay paused.
func TestWakeAgentsTheLastPauseLeftBehind(t *testing.T) {
	now := time.Now().UTC()
	b := aiControlSetup(t, 67, now.Add(48*time.Hour))
	pausedAt := now.Add(-time.Hour)
	os.WriteFile(aiPausedPath(), []byte("Paused by the owner"), 0600)
	os.Chtimes(aiPausedPath(), pausedAt, pausedAt)
	r := asUser(t, &Deps{}, ownerUser)
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/resume", ""); w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	// The pause loop finished after the resume.
	b.agents = []paperclip.Agent{{ID: "pr", Name: "Prospect Research", Status: "paused"}, {ID: "old", Name: "Old", Status: "paused"}, {ID: "x", Name: "X", Status: "idle"}}
	b.pauses = map[string]time.Time{"pr": pausedAt.Add(20 * time.Second), "old": pausedAt.Add(-72 * time.Hour)}
	b.resumed = nil

	if w := do(t, r, http.MethodGet, "/api/founderos/pages/usage/ai", ""); strings.Contains(w.Body.String(), "stillPaused") {
		t.Fatalf("the 30-second poll must not read the board: %s", w.Body)
	}
	w := do(t, r, http.MethodGet, "/api/founderos/pages/usage/ai?agents=1", "")
	if !strings.Contains(w.Body.String(), `"stillPaused":["Prospect Research"]`) {
		t.Fatalf("status: %s", w.Body)
	}
	w = do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/wake", "")
	if w.Code != 200 || len(b.resumed) != 1 || b.resumed[0] != "pr" {
		t.Fatalf("wake: %d %s woke=%v", w.Code, w.Body, b.resumed)
	}
}

// 31 Oct–1 Nov: the shared notify loop stalled, the 4-hour lease lapsed, and
// the sales kit wrote a pause while the owner's go-ahead still held. The
// go-ahead itself must keep AI resumed in-process.
func TestAnActiveGoAheadOutlivesALapsedLease(t *testing.T) {
	now := time.Now().UTC()
	aiControlSetup(t, 67, now.Add(48*time.Hour))
	r := asUser(t, &Deps{}, ownerUser)
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/resume", ""); w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	os.Remove(aiResumePath())
	if !aiResumeActive(now.Add(5 * time.Hour)) {
		t.Fatal("go-ahead ignored once the lease lapsed")
	}
	if err := salesKitAIAllowed(); err != nil {
		t.Fatalf("sales kit paused AI under an active go-ahead: %v", err)
	}
	if _, err := os.Stat(aiPausedPath()); err == nil {
		t.Fatal("a pause was written under an active go-ahead")
	}
}

// 2 Nov: after the weekly reset the newest snapshot still said 67% for the
// window that had ended. An expired window is unknown, not the old percent.
func TestAnExpiredWeeklyWindowIsNotCurrent(t *testing.T) {
	now := time.Date(2026, 11, 2, 17, 0, 0, 0, time.UTC)
	past, future := "2026-11-02T10:24:44Z", "2026-11-09T10:24:44Z"
	if _, _, ok := currentWeekly(&devicepush.OfficialWindow{UsedPercent: 67, ResetsAt: &past}, now); ok {
		t.Fatal("expired window read as current")
	}
	pct, reset, ok := currentWeekly(&devicepush.OfficialWindow{UsedPercent: 12, ResetsAt: &future}, now)
	if !ok || pct != 12 || reset.Format(time.RFC3339) != future {
		t.Fatalf("current window: %v %v %v", pct, reset, ok)
	}
	if _, _, ok := currentWeekly(nil, now); ok {
		t.Fatal("missing window read as current")
	}
}

// Right after a weekly reset no Codex snapshot exists until Codex runs. Under
// the owner's go-ahead that must not block work; without it, it still does.
func TestAnUnknownMeterNeedsTheOwnersGoAhead(t *testing.T) {
	now := time.Now().UTC()
	aiControlSetup(t, 0, time.Time{})
	codexWeekly = func(time.Time) (float64, bool) { return 0, false }
	codexWeeklyWindow = func(time.Time) (float64, time.Time, bool) { return 0, time.Time{}, false }
	if salesKitAIAllowed() == nil {
		t.Fatal("unknown meter allowed without a go-ahead")
	}
	r := asUser(t, &Deps{}, ownerUser)
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/resume", ""); w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	if err := salesKitAIAllowed(); err != nil {
		t.Fatalf("go-ahead ignored with an unknown meter: %v", err)
	}
	if _, err := taskExecutionContext(); err != nil && strings.Contains(err.Error(), "weekly usage") {
		t.Fatalf("task context blocked on the unknown meter: %v", err)
	}
	_ = now
}
