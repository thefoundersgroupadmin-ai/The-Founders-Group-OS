package api

// The owner's controls for the 50% guard (/os/usage). The owner's rule
// (2026-10-28): past the cutoff one go-ahead is enough. Resume records that
// go-ahead until the weekly window resets, wakes only the agents the guard
// paused, and from then on the guard renews its own short resume lease (the
// one agent tasks check) every pass until the reset. After the reset a new
// crossing of 50% pauses again. Pause now ends the go-ahead and pauses
// everything. Only the owner can press either.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

func init() { RegisterPage(aiControlRegister) }

const (
	aiLease       = 4 * time.Hour      // the resume lease agent tasks accept
	aiGoAheadMax  = 8 * 24 * time.Hour // one weekly window, with slack
	aiGoAheadWeek = 7 * 24 * time.Hour // when the reset time is unknown
)

type aiGoAhead struct {
	AuthorizedAt time.Time `json:"authorizedAt"`
	Until        time.Time `json:"until"`
	By           string    `json:"by"`
}

func aiGoAheadPath() string { return filepath.Join(filepath.Dir(aiPausedPath()), "ai-goahead.json") }
func aiResumePath() string  { return filepath.Join(filepath.Dir(aiPausedPath()), "ai-resume.json") }

// aiGoAheadActive is the owner's go-ahead, when it still holds: before its
// reset, and not overtaken by a later pause.
func aiGoAheadActive(now time.Time) (aiGoAhead, bool) {
	var g aiGoAhead
	raw, err := os.ReadFile(aiGoAheadPath())
	if err != nil || json.Unmarshal(raw, &g) != nil || g.AuthorizedAt.After(now) || !g.Until.After(now) || g.Until.Sub(g.AuthorizedAt) > aiGoAheadMax {
		return g, false
	}
	if info, err := os.Stat(aiPausedPath()); err == nil && info.ModTime().After(g.AuthorizedAt) {
		return g, false
	}
	return g, true
}

// renewAiLease writes the short lease the guard and agent tasks read, never
// past the go-ahead's reset.
func renewAiLease(now time.Time, g aiGoAhead) error {
	exp := now.Add(aiLease)
	if exp.After(g.Until) {
		exp = g.Until
	}
	raw, _ := json.Marshal(aiResumeGrant{AuthorizedAt: now, ExpiresAt: exp,
		Reason: fmt.Sprintf("Owner's go-ahead in TFG OS (%s, %s), until the weekly reset %s", g.By, g.AuthorizedAt.Format(time.RFC3339), g.Until.Format(time.RFC3339))})
	return os.WriteFile(aiResumePath(), raw, 0o600)
}

// codexWeeklyWindow is the official weekly percent and when that window
// resets (zero when unknown).
var codexWeeklyWindow = func(now time.Time) (float64, time.Time, bool) {
	home, _ := os.UserHomeDir()
	seat := codexSeatFor(home, now)
	if seat == nil || seat.Official == nil || seat.Official.Weekly == nil {
		return 0, time.Time{}, false
	}
	w := seat.Official.Weekly
	var reset time.Time
	if w.ResetsAt != nil {
		reset, _ = time.Parse(time.RFC3339Nano, *w.ResetsAt)
	}
	return w.UsedPercent, reset, true
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
		if g, ok := aiGoAheadActive(now); ok {
			out["goAheadUntil"] = g.Until
		}
		c.JSON(http.StatusOK, out)
	})

	s.POST("/pages/usage/ai/resume", func(c *gin.Context) {
		p, ok := aiOwner(c)
		if !ok {
			return
		}
		now := time.Now().UTC()
		until := now.Add(aiGoAheadWeek)
		if _, reset, ok := codexWeeklyWindow(now); ok && reset.After(now) && reset.Sub(now) <= aiGoAheadMax {
			until = reset
		}
		// When the guard paused: agents it paused from then on are the ones to wake.
		var pausedSince time.Time
		if info, err := os.Stat(aiPausedPath()); err == nil {
			pausedSince = info.ModTime().Add(-2 * time.Minute)
			if err := os.Rename(aiPausedPath(), aiPausedPath()+".cleared-"+now.Format("20060102-150405")); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "could not clear the pause: " + err.Error()})
				return
			}
		}
		g := aiGoAhead{AuthorizedAt: now, Until: until, By: p.Email}
		raw, _ := json.Marshal(g)
		if err := os.WriteFile(aiGoAheadPath(), raw, 0o600); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "could not record the go-ahead: " + err.Error()})
			return
		}
		if err := renewAiLease(now, g); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "could not record the go-ahead: " + err.Error()})
			return
		}

		resumed, kept, failed := []string{}, []string{}, []string{}
		var boardErr error
		if !pausedSince.IsZero() {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
			defer cancel()
			b := aiBoard(d)
			agents, err := b.Agents(ctx)
			pauses, perr := b.AgentPauses(ctx)
			boardErr = errors.Join(err, perr)
			if boardErr == nil {
				for _, a := range agents {
					if a.Status != "paused" {
						continue
					}
					if at, ok := pauses[a.ID]; !ok || at.Before(pausedSince) {
						kept = append(kept, agentName(a))
						continue
					}
					if _, err := b.ResumeAgent(ctx, a.ID); err != nil {
						failed = append(failed, agentName(a))
						continue
					}
					resumed = append(resumed, agentName(a))
				}
			}
		}
		out := gin.H{"ok": true, "until": until, "resumed": resumed, "kept": kept, "failed": failed}
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
		_ = os.Remove(aiGoAheadPath())
		_ = os.Remove(aiResumePath())
		why := fmt.Sprintf("Paused by %s in TFG OS at %s; all agents paused until you resume.", p.Email, now.Format("Mon 15:04"))
		if err := os.WriteFile(aiPausedPath(), []byte(why+"\n"), 0o600); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "could not pause: " + err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		b := aiBoard(d)
		paused := []string{}
		agents, err := b.Agents(ctx)
		for _, a := range agents {
			if a.Status != "paused" {
				if _, perr := b.PauseAgent(ctx, a.ID); perr == nil {
					paused = append(paused, agentName(a))
				}
			}
		}
		out := gin.H{"ok": true, "paused": paused}
		if err != nil {
			out["warning"] = "AI work is paused, but the agent board could not be read: " + err.Error()
		}
		c.JSON(http.StatusOK, out)
	})
}
