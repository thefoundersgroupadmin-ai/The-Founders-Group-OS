package api

// The owner's controls for the 50% guard (/os/usage). The owner's rule
// (2026-10-28): past the cutoff one go-ahead is enough, until the meter next
// crosses 50% after a weekly reset. Resume records that go-ahead and wakes
// only the agents the guard paused; the guard renews its short resume lease
// (the one agent tasks check) every pass while the go-ahead holds. A reading
// at or over 50% in a weekly window that began after the go-ahead ends it and
// the guard pauses again. Pause now ends the go-ahead and pauses everything.
// Only the owner can press either.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

func init() { RegisterPage(aiControlRegister) }

const (
	aiLease = 4 * time.Hour      // the resume lease agent tasks accept
	aiWeek  = 7 * 24 * time.Hour // Codex's weekly window (10080 minutes)
)

type aiGoAhead struct {
	AuthorizedAt time.Time `json:"authorizedAt"`
	By           string    `json:"by"`
}

// aiStateMu orders the pause flag and go-ahead writes (Resume, Pause now, the
// guard) within this process.
var aiStateMu sync.Mutex

func aiGoAheadPath() string { return filepath.Join(filepath.Dir(aiPausedPath()), "ai-goahead.json") }
func aiResumePath() string  { return filepath.Join(filepath.Dir(aiPausedPath()), "ai-resume.json") }

func readAiGoAhead() (aiGoAhead, bool) {
	var g aiGoAhead
	raw, err := os.ReadFile(aiGoAheadPath())
	return g, err == nil && json.Unmarshal(raw, &g) == nil && !g.AuthorizedAt.IsZero()
}

// aiCrossedSince: the current reading is at or over the cutoff in a weekly
// window that began after at. A reading without a reset time cannot be placed
// in a window and does not end a go-ahead (official snapshots carry one).
func aiCrossedSince(at, now time.Time) bool {
	pct, reset, ok := codexWeeklyWindow(now)
	return ok && pct >= aiCutoffPercent && !reset.IsZero() && reset.Add(-aiWeek).After(at)
}

// aiGoAheadActive is the owner's go-ahead while it holds: not overtaken by a
// later pause and no crossing of the cutoff in a later weekly window.
func aiGoAheadActive(now time.Time) (aiGoAhead, bool) {
	g, ok := readAiGoAhead()
	if !ok || g.AuthorizedAt.After(now) || aiCrossedSince(g.AuthorizedAt, now) {
		return g, false
	}
	if info, err := os.Stat(aiPausedPath()); err == nil && info.ModTime().After(g.AuthorizedAt) {
		return g, false
	}
	return g, true
}

// aiGoAheadEnded: a recorded go-ahead whose next crossing has come. Its lease
// must not carry work past the cutoff.
func aiGoAheadEnded(now time.Time) bool {
	g, ok := readAiGoAhead()
	return ok && !g.AuthorizedAt.After(now) && aiCrossedSince(g.AuthorizedAt, now)
}

// renewAiLease writes the short lease the guard and agent tasks read.
func renewAiLease(now time.Time, g aiGoAhead) error {
	raw, _ := json.Marshal(aiResumeGrant{AuthorizedAt: now, ExpiresAt: now.Add(aiLease),
		Reason: fmt.Sprintf("Owner's go-ahead in TFG OS (%s, %s), until usage next reaches %d%% after a weekly reset", g.By, g.AuthorizedAt.Format(time.RFC3339), int(aiCutoffPercent))})
	return os.WriteFile(aiResumePath(), raw, 0o600)
}

// codexWeeklyWindow is the official weekly percent and when that window
// resets (zero when unknown).
var codexWeeklyWindow = func(now time.Time) (float64, time.Time, bool) {
	home, _ := os.UserHomeDir()
	seat := codexSeatFor(home, now)
	if seat == nil || seat.Official == nil {
		return 0, time.Time{}, false
	}
	return currentWeekly(seat.Official.Weekly, now)
}

type aiBoardAPI interface {
	agentPauser
	ResumeAgent(ctx context.Context, agentID string) (bool, error)
	AgentPauses(ctx context.Context) (map[string]time.Time, error)
}

// aiBoard is the Paperclip board; a var so tests never reach the network.
var aiBoard = func(d *Deps) aiBoardAPI { return paperclip.New(d.Resolver) }

func aiOwner(c *gin.Context) (people.Person, bool) {
	p, ok := people.From(c.Request.Context())
	if !ok || !p.Owner {
		c.JSON(http.StatusForbidden, gin.H{"ok": false, "error": "only the owner can resume or pause AI work"})
		return p, false
	}
	return p, true
}

func agentName(a paperclip.Agent) string {
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

// lastPauseClearedSince is when the pause the owner last resumed began (the
// cleared flag keeps its time), less the same two minutes Resume allows.
func lastPauseClearedSince() (time.Time, bool) {
	matches, _ := filepath.Glob(aiPausedPath() + ".cleared-*")
	var newest time.Time
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest.Add(-2 * time.Minute), !newest.IsZero()
}

// wakePausedSince resumes (or with wake=false only lists) paused agents whose
// last pause came at or after since; agents paused earlier are kept.
func wakePausedSince(ctx context.Context, b aiBoardAPI, since time.Time, wake bool) (resumed, kept, failed []string, err error) {
	resumed, kept, failed = []string{}, []string{}, []string{}
	agents, err := b.Agents(ctx)
	pauses, perr := b.AgentPauses(ctx)
	if err = errors.Join(err, perr); err != nil {
		return
	}
	for _, a := range agents {
		if a.Status != "paused" {
			continue
		}
		if at, ok := pauses[a.ID]; !ok || at.Before(since) {
			kept = append(kept, agentName(a))
			continue
		}
		if wake {
			if _, err := b.ResumeAgent(ctx, a.ID); err != nil {
				failed = append(failed, agentName(a))
				continue
			}
		}
		resumed = append(resumed, agentName(a))
	}
	return
}

func aiControlRegister(s *gin.RouterGroup, d *Deps) {
	s.GET("/pages/usage/ai", func(c *gin.Context) {
		now := time.Now()
		c.Header("Cache-Control", "no-store")
		p, _ := people.From(c.Request.Context())
		out := gin.H{"cutoff": aiCutoffPercent, "paused": aiPaused() != "", "reason": aiPaused(), "canControl": p.Owner}
		if pct, reset, ok := codexWeeklyWindow(now); ok {
			out["usedPercent"] = pct
			if !reset.IsZero() {
				out["resetsAt"] = reset
			}
		}
		if _, ok := aiGoAheadActive(now); ok {
			out["goAhead"] = true
			// Board lookups only when asked (page open, after an action), not
			// on every 30-second poll: Paperclip's database is small.
			if since, ok := lastPauseClearedSince(); ok && p.Owner && c.Query("agents") == "1" {
				ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
				defer cancel()
				if left, _, _, err := wakePausedSince(ctx, aiBoard(d), since, false); err == nil && len(left) > 0 {
					out["stillPaused"] = left
				}
			}
		}
		c.JSON(http.StatusOK, out)
	})

	// Wakes the agents the last pause caught after the owner resumed (Pause
	// now pauses one agent at a time, so a quick Resume can overtake it).
	s.POST("/pages/usage/ai/wake", func(c *gin.Context) {
		if _, ok := aiOwner(c); !ok {
			return
		}
		since, ok := lastPauseClearedSince()
		if _, active := aiGoAheadActive(time.Now()); !active || !ok {
			c.JSON(http.StatusConflict, gin.H{"ok": false, "error": "AI work is not resumed; press Resume first"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		resumed, kept, failed, err := wakePausedSince(ctx, aiBoard(d), since, true)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": "the agent board could not be read: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "resumed": resumed, "kept": kept, "failed": failed})
	})

	s.POST("/pages/usage/ai/resume", func(c *gin.Context) {
		p, ok := aiOwner(c)
		if !ok {
			return
		}
		now := time.Now().UTC()
		aiStateMu.Lock()
		// When the guard paused: agents it paused from then on are the ones to wake.
		var pausedSince time.Time
		if info, err := os.Stat(aiPausedPath()); err == nil {
			pausedSince = info.ModTime().Add(-2 * time.Minute)
			if err := os.Rename(aiPausedPath(), aiPausedPath()+".cleared-"+now.Format("20060102-150405")); err != nil {
				aiStateMu.Unlock()
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "could not clear the pause: " + err.Error()})
				return
			}
		}
		g := aiGoAhead{AuthorizedAt: now, By: p.Email}
		raw, _ := json.Marshal(g)
		err := os.WriteFile(aiGoAheadPath(), raw, 0o600)
		if err == nil {
			err = renewAiLease(now, g)
		}
		aiStateMu.Unlock()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "could not record the go-ahead: " + err.Error()})
			return
		}

		resumed, kept, failed := []string{}, []string{}, []string{}
		var boardErr error
		if !pausedSince.IsZero() {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
			defer cancel()
			resumed, kept, failed, boardErr = wakePausedSince(ctx, aiBoard(d), pausedSince, true)
		}
		out := gin.H{"ok": true, "resumed": resumed, "kept": kept, "failed": failed}
		if boardErr != nil {
			out["warning"] = "AI work is resumed, but the agent board could not be read to wake agents: " + boardErr.Error()
		}
		c.JSON(http.StatusOK, out)
	})

	s.POST("/pages/usage/ai/pause", func(c *gin.Context) {
		p, ok := aiOwner(c)
		if !ok {
			return
		}
		now := time.Now()
		aiStateMu.Lock()
		_ = os.Remove(aiGoAheadPath())
		_ = os.Remove(aiResumePath())
		why := fmt.Sprintf("Paused by %s in TFG OS at %s; all agents paused until you resume.", p.Email, now.Format("Mon 15:04"))
		err := os.WriteFile(aiPausedPath(), []byte(why+"\n"), 0o600)
		aiStateMu.Unlock()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "could not pause: " + err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		b := aiBoard(d)
		paused, failed := []string{}, []string{}
		agents, err := b.Agents(ctx)
		for _, a := range agents {
			// A Resume pressed while this loop runs clears the flag: stop
			// rather than pause agents behind the owner's go-ahead.
			if _, err := os.Stat(aiPausedPath()); err != nil {
				break
			}
			if a.Status != "paused" {
				if _, perr := b.PauseAgent(ctx, a.ID); perr == nil {
					paused = append(paused, agentName(a))
				} else {
					failed = append(failed, agentName(a))
				}
			}
		}
		// The guard keeps re-pausing any agent still running while paused.
		out := gin.H{"ok": true, "paused": paused, "failed": failed}
		if err != nil {
			out["warning"] = "AI work is paused, but the agent board could not be read: " + err.Error()
		}
		c.JSON(http.StatusOK, out)
	})
}
