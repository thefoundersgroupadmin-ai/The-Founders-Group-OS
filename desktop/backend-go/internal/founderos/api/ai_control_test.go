package api

import (
	"context"
	"encoding/json"
	"errors"
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
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out.Resumed) != 1 || out.Resumed[0] != "CEO" || len(out.Kept) != 1 || out.Kept[0] != "Old" {
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
	// After the reset, crossing 50% again in the new window pauses as before.
	after := reset.Add(time.Hour)
	codexWeekly = func(time.Time) (float64, bool) { return 67, true }
	codexWeeklyWindow = func(time.Time) (float64, time.Time, bool) { return 67, reset.Add(aiWeek), true }
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

// A reading without a reset time cannot be placed in a weekly window, so it
// neither ends a go-ahead nor substitutes a fixed expiry.
func TestWithoutAKnownResetTheGoAheadHasNoFixedExpiry(t *testing.T) {
	aiControlSetup(t, 67, time.Time{})
	codexWeeklyWindow = func(time.Time) (float64, time.Time, bool) { return 67, time.Time{}, true }
	if w := do(t, asUser(t, &Deps{}, ownerUser), http.MethodPost, "/api/founderos/pages/usage/ai/resume", ""); w.Code != 200 || strings.Contains(w.Body.String(), "until") {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	if !aiResumeActive(time.Now().Add(10 * 24 * time.Hour)) {
		t.Fatal("the go-ahead expired on a fixed timer")
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

// Codex review, 2 Nov. The owner's rule: one go-ahead lasts until the meter
// next crosses 50% after a weekly reset, never a fixed time.
func TestTheGoAheadEndsAtTheNextCrossingNotAtAFixedTime(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(24 * time.Hour)
	b := aiControlSetup(t, 67, reset)
	os.WriteFile(aiPausedPath(), []byte("cutoff"), 0600)
	os.Chtimes(aiPausedPath(), now.Add(-time.Hour), now.Add(-time.Hour))
	r := asUser(t, &Deps{}, ownerUser)
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/resume", ""); w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	// Past the reset with no new reading yet: still the owner's go-ahead.
	codexWeekly = func(time.Time) (float64, bool) { return 0, false }
	codexWeeklyWindow = func(time.Time) (float64, time.Time, bool) { return 0, time.Time{}, false }
	if !aiResumeActive(reset.Add(time.Hour)) || salesKitAIAllowed() != nil {
		t.Fatal("the go-ahead ended at the reset instead of the next crossing")
	}
	// The new week below 50%: still running, nine days on.
	next := reset.Add(7 * 24 * time.Hour)
	codexWeekly = func(time.Time) (float64, bool) { return 30, true }
	codexWeeklyWindow = func(time.Time) (float64, time.Time, bool) { return 30, next, true }
	if !aiResumeActive(now.Add(9 * 24 * time.Hour)) {
		t.Fatal("a fixed expiry ended the go-ahead before any new crossing")
	}
	// The new week crosses 50%: the go-ahead and its lease end; the guard pauses.
	codexWeekly = func(time.Time) (float64, bool) { return 55, true }
	codexWeeklyWindow = func(time.Time) (float64, time.Time, bool) { return 55, next, true }
	if aiResumeActive(time.Now()) {
		t.Fatal("work continued past the next crossing (go-ahead or its lease)")
	}
	b.agents = []paperclip.Agent{{ID: "x", Status: "idle"}}
	b.paused = nil
	if evs := aiGuardPass(context.Background(), b, time.Now()); len(evs) != 1 || len(b.paused) != 1 || aiPaused() == "" {
		t.Fatalf("the next crossing must pause: %v paused=%v", evs, b.paused)
	}
}

// While paused, agents that failed to stop are paused again on the next pass,
// whatever the meter says.
func TestAPauseIsReconciledBelowTheCutoff(t *testing.T) {
	b := aiControlSetup(t, 20, time.Now().Add(48*time.Hour))
	os.WriteFile(aiPausedPath(), []byte("Paused by the owner"), 0600)
	b.agents = []paperclip.Agent{{ID: "still-running", Status: "running"}, {ID: "p", Status: "paused"}}
	aiGuardPass(context.Background(), b, time.Now())
	if len(b.paused) != 1 || b.paused[0] != "still-running" {
		t.Fatalf("a running agent survived the pause: %v", b.paused)
	}
}

// A guard pass that was already pausing when the owner resumed stops pausing
// and does not write a pause over the go-ahead.
type resumedMidGuard struct{ *controlBoard }

func (b resumedMidGuard) PauseAgent(ctx context.Context, id string) (bool, error) {
	ok, err := b.controlBoard.PauseAgent(ctx, id)
	g := aiGoAhead{AuthorizedAt: time.Now().UTC(), By: "owner@example.test"}
	raw, _ := json.Marshal(g)
	os.WriteFile(aiGoAheadPath(), raw, 0o600)
	return ok, err
}

func TestAGuardPassDoesNotOverrideAResumeMidway(t *testing.T) {
	b := aiControlSetup(t, 67, time.Now().Add(48*time.Hour))
	b.agents = []paperclip.Agent{{ID: "a", Status: "idle"}, {ID: "b", Status: "idle"}}
	evs := aiGuardPass(context.Background(), resumedMidGuard{b}, time.Now())
	if len(b.paused) != 1 || len(evs) != 0 || aiPaused() != "" {
		t.Fatalf("stale guard pass overrode the resume: paused=%v evs=%v flag=%q", b.paused, evs, aiPaused())
	}
}

// Pause now reports agents it could not stop.
type failingPause struct{ *controlBoard }

func (b failingPause) PauseAgent(ctx context.Context, id string) (bool, error) {
	if id == "stuck" {
		return false, errors.New("board timeout")
	}
	return b.controlBoard.PauseAgent(ctx, id)
}

func TestPauseNowReportsAgentsItCouldNotStop(t *testing.T) {
	b := aiControlSetup(t, 20, time.Now().Add(48*time.Hour))
	b.agents = []paperclip.Agent{{ID: "ok", Name: "OK", Status: "idle"}, {ID: "stuck", Name: "Stuck", Status: "running"}}
	aiBoard = func(*Deps) aiBoardAPI { return failingPause{b} }
	r := asUser(t, &Deps{}, ownerUser)
	w := do(t, r, http.MethodPost, "/api/founderos/pages/usage/ai/pause", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"failed":["Stuck"]`) {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}
}
