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
