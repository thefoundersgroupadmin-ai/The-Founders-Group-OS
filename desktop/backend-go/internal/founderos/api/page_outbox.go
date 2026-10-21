package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
)

// /os/outbox: agent-drafted email waiting for the operator (migration 166).
// POST .../approve is the outbound write: it sends through the comms email
// source (FOUNDEROS_WRITES guard + mail guard), the approval standing in for
// the mail guard's per-call recipient sign-off. Reject is local, not guarded.
func init() { RegisterPage(registerOutbox) }

const outboxWorkspace = "founderos"

func outboxErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, outbox.ErrNoWorkspace):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
	case errors.Is(err, outbox.ErrBadContent):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, outbox.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, outbox.ErrNotPending):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

func registerOutbox(s *gin.RouterGroup, d *Deps) {
	noPool := func(c *gin.Context) bool {
		if d.Pool == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "outbox unavailable: no Postgres"})
			return true
		}
		return false
	}
	s.GET("/pages/outbox", func(c *gin.Context) {
		if noPool(c) {
			return
		}
		page, err := outbox.Load(c.Request.Context(), d.Pool, outboxWorkspace)
		if err != nil {
			outboxErr(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
	s.POST("/pages/outbox/:id/approve", func(c *gin.Context) {
		if noPool(c) {
			return
		}
		// The body is what the operator reviewed; that, not the stored draft, is sent.
		var content outbox.Content
		if err := c.ShouldBindJSON(&content); err != nil {
			outboxErr(c, outbox.ErrBadContent)
			return
		}
		send := func(ctx context.Context, it outbox.Item) error {
			r := email.Reply{To: it.To, Subject: it.Subject, Text: it.Body, RecipientApproved: true}
			if it.AccountID != nil {
				r.AccountID = *it.AccountID
			}
			err := commsSourcesFor(d).SendEmail(ctx, r)
			if errors.Is(err, email.ErrDeliveryUncertain) {
				return fmt.Errorf("%w: %v", outbox.ErrUncertain, err)
			}
			return err
		}
		it, err := outbox.Approve(c.Request.Context(), d.Pool, outboxWorkspace, c.Param("id"), content, send)
		if err != nil && it != nil && (it.Status == "failed" || it.Status == "uncertain") {
			// The outcome is recorded (failed: retryable; uncertain: check first); say why.
			c.JSON(http.StatusBadGateway, gin.H{"ok": false, "item": it, "detail": err.Error(),
				"guarded": errors.Is(err, guard.ErrWritesDisabled)})
			return
		}
		if err != nil && it != nil {
			// Sent, but a follow-up record failed: report both.
			c.JSON(http.StatusOK, gin.H{"ok": true, "item": it, "warning": err.Error()})
			return
		}
		if err != nil {
			outboxErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "item": it})
	})
	s.POST("/pages/outbox/:id/reject", func(c *gin.Context) {
		if noPool(c) {
			return
		}
		it, err := outbox.Reject(c.Request.Context(), d.Pool, outboxWorkspace, c.Param("id"))
		if err != nil {
			outboxErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "item": it})
	})
}
