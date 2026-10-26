package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
	"github.com/rhl/businessos-backend/internal/founderos/people"
	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
)

func init() { RegisterPage(registerSalesAutomation) }

var salesAutomationTickMu sync.Mutex

func startSalesAutomation(d *Deps) {
	if d.Pool == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			salesAutomationTick(d)
		}
	}()
}
func salesAutomationTick(d *Deps) {
	if !guard.CronsEnabled() || d.Pool == nil {
		return
	}
	if !salesAutomationTickMu.TryLock() {
		return
	}
	defer salesAutomationTickMu.Unlock()
	if err := salesKitAIAllowed(); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store := saleskit.AutomationStore{Pool: d.Pool}
	// Only installation owners can configure these routines; each config retains
	// the authorising person's identity for the text-only Claude runner.
	configs, err := store.Workspaces(ctx, "", true)
	if err != nil {
		slog.Warn("sales automation: configuration unavailable", "error", err)
		return
	}
	for _, c := range configs {
		if !c.Enabled || c.UserID == "" {
			continue
		}
		owner := people.OwnerEmail() == "" || people.IsOwner(c.UserEmail)
		if !owner {
			continue
		} // changing the installation owner revokes old schedules
		if err := salesKitAIAllowed(); err != nil {
			return
		}
		snapshot, err := store.Snapshot(ctx, c.WorkspaceID)
		if err != nil {
			slog.Warn("sales automation: sources unavailable", "workspace", c.Workspace, "error", err)
			continue
		}
		zone, err := time.LoadLocation(c.Timezone)
		if err != nil {
			continue
		}
		now := time.Now().In(zone)
		plan := saleskit.Plan(now, c.Checkpoint, snapshot)
		if len(plan.Skills) == 0 {
			continue
		}
		prompt, err := saleskit.AutomationPrompt(c.Workspace, c.Profile, snapshot, plan, now)
		if err != nil {
			slog.Warn("sales automation: invalid packet", "workspace", c.Workspace, "error", err)
			continue
		}
		id, claimed, err := store.Claim(ctx, c, plan, snapshot)
		if err != nil || !claimed {
			continue
		}
		// Re-check immediately before inference. No provider switching or retries.
		runErr := salesKitAIAllowed()
		answer := ""
		if runErr == nil {
			runCtx, stop := context.WithTimeout(people.With(ctx, people.Person{ID: c.UserID, Email: c.UserEmail, Name: c.UserName, Owner: owner}), 95*time.Second)
			answer, runErr = salesKitModelFor(d).Complete(runCtx, prompt)
			stop()
			if runErr == nil && strings.TrimSpace(answer) == "" {
				runErr = errors.New("sales model returned no analysis")
			}
		}
		safeErr := runErr
		if runErr != nil {
			safeErr = errors.New("Automatic sales analysis did not complete. Check your AI account and usage pause. The same source packet is not automatically retried.")
		}
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = store.Finish(finishCtx, c, id, plan.Next, answer, safeErr)
		finishCancel()
		if err != nil {
			slog.Error("sales automation: could not record result", "run", id, "error", err)
		}
	}
}

func registerSalesAutomation(s *gin.RouterGroup, d *Deps) {
	load := func(c *gin.Context) (people.Person, []saleskit.AutomationConfig, bool) {
		p, ok := personOf(c)
		if !ok {
			return p, nil, false
		}
		if d.Pool == nil {
			code := 200
			if c.Request.Method != "GET" {
				code = 503
			}
			c.JSON(code, gin.H{"configured": false, "workspaces": []any{}, "runs": []any{}, "detail": "Sales automation needs the OS database."})
			return p, nil, false
		}
		configs, err := (saleskit.AutomationStore{Pool: d.Pool}).Workspaces(c.Request.Context(), p.ID, p.Owner)
		if err != nil {
			c.JSON(503, gin.H{"error": "Sales automation storage is unavailable. Apply the backend database migration."})
			return p, nil, false
		}
		return p, configs, true
	}
	s.GET("/pages/sales-kit/automation", func(c *gin.Context) {
		p, configs, ok := load(c)
		if !ok {
			return
		}
		selected := c.Query("workspace")
		var chosen *saleskit.AutomationConfig
		for i := range configs {
			if configs[i].WorkspaceID == selected || (selected == "" && chosen == nil) {
				chosen = &configs[i]
			}
		}
		runs := []saleskit.AutomationRun{}
		gaps := []string{}
		hasSources := false
		if chosen != nil {
			store := saleskit.AutomationStore{Pool: d.Pool}
			var err error
			runs, err = store.Runs(c.Request.Context(), chosen.WorkspaceID)
			if err != nil {
				c.JSON(503, gin.H{"error": "Sales run history is unavailable"})
				return
			}
			snapshot, err := store.Snapshot(c.Request.Context(), chosen.WorkspaceID)
			if err != nil {
				gaps = append(gaps, "Synced OS sales records are unavailable")
			} else {
				gaps = snapshot.Gaps
				hasSources = len(snapshot.Sources) > 0
			}
		} else if selected != "" {
			c.JSON(404, gin.H{"error": "workspace not found"})
			return
		}
		c.JSON(200, gin.H{"configured": true, "workspaces": configs, "selected": chosen, "runs": runs, "canConfigure": p.Owner,
			"schedulesEnabled": guard.CronsEnabled(), "paused": aiPaused() != "", "hasSources": hasSources, "gaps": gaps})
	})
	s.PUT("/pages/sales-kit/automation", func(c *gin.Context) {
		p, configs, ok := load(c)
		if !ok {
			return
		}
		if !p.Owner {
			c.JSON(403, gin.H{"error": "The installation owner configures automatic sales work"})
			return
		}
		var body struct {
			WorkspaceID string `json:"workspaceId"`
			Profile     string `json:"profile"`
			Timezone    string `json:"timezone"`
			Enabled     bool   `json:"enabled"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 30000)
		dec := json.NewDecoder(c.Request.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			c.JSON(400, gin.H{"error": "invalid automation configuration"})
			return
		}
		var chosen *saleskit.AutomationConfig
		for i := range configs {
			if configs[i].WorkspaceID == body.WorkspaceID {
				chosen = &configs[i]
				break
			}
		}
		if chosen == nil {
			c.JSON(404, gin.H{"error": "workspace not found"})
			return
		}
		chosen.Profile = body.Profile
		chosen.Enabled = body.Enabled
		chosen.Timezone = body.Timezone
		chosen.UserID = p.ID
		chosen.UserEmail = p.Email
		chosen.UserName = p.Name
		if strings.TrimSpace(chosen.Profile) == "" || len(chosen.Profile) > 24000 {
			c.JSON(400, gin.H{"error": "Set the client sales profile once (max 24000 bytes)"})
			return
		}
		if _, err := time.LoadLocation(chosen.Timezone); err != nil {
			c.JSON(400, gin.H{"error": "Choose a valid timezone"})
			return
		}
		if err := (saleskit.AutomationStore{Pool: d.Pool}).Save(c.Request.Context(), *chosen); err != nil {
			c.JSON(503, gin.H{"error": "Could not save sales automation"})
			return
		}
		c.JSON(200, gin.H{"saved": true, "enabled": chosen.Enabled, "schedulesEnabled": guard.CronsEnabled()})
		if chosen.Enabled {
			go salesAutomationTick(d)
		}
	})
}
