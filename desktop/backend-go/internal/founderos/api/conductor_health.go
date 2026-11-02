package api

// Is anyone there to answer the Conductor? A message to the cockpit always
// posts fine; the CEO agent that answers it can still be failing every run
// (a Node too old for its engine, a login gone, a paused seat). Without this
// the chat just waits in silence. The panel shows it, the phone is told once
// per new problem, and "try again" clears the seat's error and wakes it.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/notify"
)

type conductorHealth struct {
	OK      bool   `json:"ok"`
	Agent   string `json:"agent,omitempty"`
	AgentID string `json:"agentId,omitempty"`
	Problem string `json:"problem,omitempty"`
	Since   string `json:"since,omitempty"`
}

// isConductorSeat: the seats that answer the cockpit.
func isConductorSeat(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "ceo" || n == "conductor"
}

// healthOf reads the answering seat's state and its own latest runs.
func healthOf(agents []paperclip.Agent, runs []paperclip.FailoverRun) conductorHealth {
	var seat *paperclip.Agent
	for i := range agents {
		if isConductorSeat(agents[i].Name) {
			seat = &agents[i]
			if agents[i].Status != "error" && agents[i].Status != "paused" {
				break // a healthy seat answers; prefer it over a broken twin
			}
		}
	}
	if seat == nil {
		return conductorHealth{OK: true}
	}
	h := conductorHealth{OK: true, Agent: seat.Name, AgentID: seat.ID}
	var mine []paperclip.FailoverRun
	for _, r := range runs {
		if r.AgentID == seat.ID && r.Status != "running" && r.Status != "queued" {
			mine = append(mine, r)
		}
	}
	at := func(r paperclip.FailoverRun) string {
		if r.FinishedAt != nil {
			return *r.FinishedAt
		}
		return ""
	}
	sort.SliceStable(mine, func(i, j int) bool { return at(mine[i]) > at(mine[j]) })
	lastErr := func() (string, string) {
		for _, r := range mine {
			if r.Status == "failed" || r.Status == "timed_out" {
				msg := "its last run failed"
				if r.Error != nil && strings.TrimSpace(*r.Error) != "" {
					msg = strings.TrimSpace(*r.Error)
				}
				return msg, at(r)
			}
			if r.Status == "succeeded" {
				break
			}
		}
		return "", ""
	}
	switch {
	case seat.Status == "paused":
		h.OK, h.Problem = false, seat.Name+" is paused, so nothing answers the Conductor."
	case seat.Status == "error":
		h.OK = false
		msg, since := lastErr()
		if msg == "" {
			msg = "it is stuck in an error state"
		}
		h.Problem, h.Since = msg, since
	case len(mine) >= 2 && mine[0].Status == "failed" && mine[1].Status == "failed":
		h.OK = false
		h.Problem, h.Since = lastErr()
	}
	if len(h.Problem) > 400 {
		h.Problem = h.Problem[:400] + "…"
	}
	return h
}

var healthCache struct {
	sync.Mutex
	at time.Time
	h  conductorHealth
}

// conductorHealthFor is polled with the chat: read the board at most every 20s.
var conductorHealthFor = func(ctx context.Context, d *Deps) conductorHealth {
	healthCache.Lock()
	defer healthCache.Unlock()
	if time.Since(healthCache.at) < 20*time.Second {
		return healthCache.h
	}
	b := paperclip.New(d.Resolver)
	agents, err := b.Agents(ctx)
	if err != nil {
		return conductorHealth{OK: true} // the thread call reports an unreachable board itself
	}
	runs, _ := b.FailoverRuns(ctx, 60)
	healthCache.h, healthCache.at = healthOf(agents, runs), time.Now()
	return healthCache.h
}

func forgetConductorHealth() {
	healthCache.Lock()
	healthCache.at = time.Time{}
	healthCache.Unlock()
}

// conductorRetry clears the answering seat's error and wakes it, so the
// waiting message gets picked up once the cause is fixed.
func registerConductorHealth(s gin.IRoutes, d *Deps) {
	s.POST("/pages/conductor/retry", func(c *gin.Context) {
		ctx := c.Request.Context()
		forgetConductorHealth()
		h := conductorHealthFor(ctx, d)
		if h.AgentID == "" {
			c.JSON(http.StatusOK, gin.H{"health": h, "retried": false})
			return
		}
		b := paperclip.New(d.Resolver)
		cockpit := b.RepairCockpitIssue(ctx, "")
		cleared, err := b.ClearAgentError(ctx, h.AgentID)
		if err != nil {
			writeError(c, err)
			return
		}
		woken, _ := b.InvokeHeartbeat(ctx, h.AgentID)
		forgetConductorHealth()
		c.JSON(http.StatusOK, gin.H{"retried": true, "cleared": cleared, "woken": woken, "cockpit": cockpit})
	})
}

type cockpitRepairer interface {
	RepairCockpitIssue(ctx context.Context, activeCeoID string) string
}

// conductorWatch runs with the notify watcher: keeps the thread unblocked,
// tells the phone once per new problem, and while the CEO stays down clears
// its error and wakes it once an hour, so it recovers by itself once the cause
// is fixed (a failed preflight costs no tokens).
type conductorWatch struct {
	lastProblem string
	lastRetry   time.Time
}

func (w *conductorWatch) pass(ctx context.Context, d *Deps, now time.Time) []notify.Event {
	b := paperclip.New(d.Resolver)
	b.RepairCockpitIssue(ctx, "")
	forgetConductorHealth()
	h := conductorHealthFor(ctx, d)
	return w.step(h, now, func() {
		if ok, _ := b.ClearAgentError(ctx, h.AgentID); ok {
			_, _ = b.InvokeHeartbeat(ctx, h.AgentID)
		}
	})
}

// step is the decision, apart from the board, for tests.
func (w *conductorWatch) step(h conductorHealth, now time.Time, retry func()) []notify.Event {
	var evs []notify.Event
	if h.OK {
		if w.lastProblem != "" {
			evs = append(evs, notify.Event{ID: fmt.Sprintf("conductor-back-%d", now.Unix()), Kind: notify.KindHealth,
				Title: "The Conductor is answering again", Body: h.Agent + " is back.", URL: "/os"})
		}
		w.lastProblem = ""
		return evs
	}
	if h.Problem != w.lastProblem {
		evs = append(evs, notify.Event{ID: fmt.Sprintf("conductor-down-%d", now.Unix()), Kind: notify.KindHealth,
			Title: "The Conductor can't answer", Body: notifyClip(h.Agent+": "+h.Problem, 180), URL: "/os"})
		w.lastProblem = h.Problem
		w.lastRetry = now // the board just failed it: give a fix time before retrying
		return evs
	}
	if h.AgentID != "" && now.Sub(w.lastRetry) >= time.Hour {
		w.lastRetry = now
		retry()
	}
	return evs
}
