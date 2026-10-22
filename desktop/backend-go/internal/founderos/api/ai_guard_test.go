package api

import (
	"context"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
)

type pauseBoard struct {
	agents []paperclip.Agent
	paused []string
}

func (b *pauseBoard) Agents(context.Context) ([]paperclip.Agent, error) { return b.agents, nil }
func (b *pauseBoard) PauseAgent(_ context.Context, id string) (bool, error) {
	b.paused = append(b.paused, id)
	return true, nil
}

func TestAtFiftyPercentEveryAgentPausesAndStaysPaused(t *testing.T) {
	peopleSetup(t)
	pct := 49.0
	old := codexWeekly
	codexWeekly = func(time.Time) (float64, bool) { return pct, true }
	defer func() { codexWeekly = old }()
	b := &pauseBoard{agents: []paperclip.Agent{{ID: "ceo", Status: "idle"}, {ID: "pr", Status: "running"}, {ID: "x", Status: "paused"}}}
	now := time.Now()

	if evs := aiGuardPass(context.Background(), b, now); len(evs) != 0 || len(b.paused) != 0 || aiPaused() != "" {
		t.Fatalf("under the cutoff nothing happens: %v %v", evs, b.paused)
	}
	pct = 50
	evs := aiGuardPass(context.Background(), b, now)
	if len(evs) != 1 || evs[0].Title != "AI work paused at 50%" || len(b.paused) != 2 || aiPaused() == "" {
		t.Fatalf("at the cutoff: %+v paused=%v flag=%q", evs, b.paused, aiPaused())
	}
	// A reset does not resume: the flag stays, and no new alert nags.
	pct = 2
	if evs := aiGuardPass(context.Background(), b, now.Add(time.Hour)); len(evs) != 0 || aiPaused() == "" {
		t.Fatalf("a reset must not resume: %v %q", evs, aiPaused())
	}
	// The Conductor's hourly retry stays off while paused.
	w := &conductorWatch{}
	down := conductorHealth{Agent: "CEO", AgentID: "ceo", Problem: "x"}
	retries := 0
	w.step(down, now, func() { retries++ })
	w.step(down, now.Add(2*time.Hour), func() { retries++ })
	if retries != 0 {
		t.Fatal("woke an agent while AI work is paused")
	}
}
