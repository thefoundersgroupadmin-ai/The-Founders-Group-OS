package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
)

// /os/approvals: everything waiting on the operator in one place, so nothing
// needs the Paperclip UI. The board's pending review requests and agent
// questions, suggested tasks, checklists and item verdicts (accept / send
// back / answer / judge), the task's documents and thread
// for context, an instruction box on any task, and the outbox count. Every
// decision is a guarded board write.
func init() { RegisterPage(registerApprovals) }

type approvalsSource interface {
	PendingInteractions(ctx context.Context) (*paperclip.Inbox, error)
	IssueDocuments(ctx context.Context, issueID string) ([]paperclip.Document, error)
	IssueThread(ctx context.Context, issueID string, limit int) ([]paperclip.Comment, error)
	AcceptInteraction(ctx context.Context, issueID, interactionID string, choice paperclip.AcceptChoice) error
	SubmitVerdicts(ctx context.Context, issueID, interactionID string, verdicts []paperclip.Verdict) error
	RejectInteraction(ctx context.Context, issueID, interactionID, reason string) error
	RespondInteraction(ctx context.Context, issueID, interactionID string, answers []paperclip.Answer) error
	CommentOnIssue(ctx context.Context, issueID, message string) (*paperclip.Comment, error)
}

// approvalsBoardFor is swapped in tests.
var approvalsBoardFor = func(d *Deps) approvalsSource { return paperclip.New(d.Resolver) }

func approvalsWriteErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, paperclip.ErrBadID):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, guard.ErrWritesDisabled):
		c.JSON(http.StatusConflict, gin.H{"ok": false, "guarded": true, "detail": err.Error()})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"ok": false, "detail": err.Error()})
	}
}

func registerApprovals(s *gin.RouterGroup, d *Deps) {
	s.GET("/pages/approvals", func(c *gin.Context) {
		ctx := c.Request.Context()
		out := gin.H{"items": []paperclip.Interaction{}, "warnings": []string{}, "outboxPending": 0}
		box, err := approvalsBoardFor(d).PendingInteractions(ctx)
		if err != nil {
			out["boardError"] = err.Error()
		} else {
			out["items"], out["warnings"] = box.Items, box.Warnings
		}
		if d.Pool != nil {
			if page, err := outbox.Load(ctx, d.Pool, outboxWorkspace); err == nil {
				out["outboxPending"] = len(page.Pending)
			}
		}
		c.JSON(http.StatusOK, out)
	})
	s.GET("/pages/approvals/issues/:issueId", func(c *gin.Context) {
		ctx, board, id := c.Request.Context(), approvalsBoardFor(d), c.Param("issueId")
		docs, err := board.IssueDocuments(ctx, id)
		if err != nil {
			approvalsWriteErr(c, err)
			return
		}
		thread, err := board.IssueThread(ctx, id, 20)
		if err != nil {
			approvalsWriteErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"documents": docs, "thread": thread})
	})
	s.POST("/pages/approvals/issues/:issueId/interactions/:interactionId/accept", func(c *gin.Context) {
		// Body is optional: suggested tasks to create, or ticked checkboxes.
		var choice paperclip.AcceptChoice
		if c.Request.ContentLength != 0 {
			if err := c.ShouldBindJSON(&choice); err != nil || len(choice.SelectedClientKeys) > 50 || len(choice.SelectedOptionIDs) > 200 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "accept takes optional selectedClientKeys (max 50) and selectedOptionIds"})
				return
			}
		}
		if err := approvalsBoardFor(d).AcceptInteraction(c.Request.Context(), c.Param("issueId"), c.Param("interactionId"), choice); err != nil {
			approvalsWriteErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	s.POST("/pages/approvals/issues/:issueId/interactions/:interactionId/reject", func(c *gin.Context) {
		var body struct {
			Reason string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || len([]rune(body.Reason)) > 4000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "reason must be at most 4000 characters"})
			return
		}
		if err := approvalsBoardFor(d).RejectInteraction(c.Request.Context(), c.Param("issueId"), c.Param("interactionId"), body.Reason); err != nil {
			approvalsWriteErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	s.POST("/pages/approvals/issues/:issueId/interactions/:interactionId/respond", func(c *gin.Context) {
		var body struct {
			Answers []paperclip.Answer `json:"answers"`
		}
		// Optional questions may be left out, so zero answers is valid.
		if err := c.ShouldBindJSON(&body); err != nil || len(body.Answers) > 20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "answers must list at most 20 questions"})
			return
		}
		if err := approvalsBoardFor(d).RespondInteraction(c.Request.Context(), c.Param("issueId"), c.Param("interactionId"), body.Answers); err != nil {
			approvalsWriteErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	s.POST("/pages/approvals/issues/:issueId/interactions/:interactionId/verdicts", func(c *gin.Context) {
		var body struct {
			Verdicts []paperclip.Verdict `json:"verdicts"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || len(body.Verdicts) == 0 || len(body.Verdicts) > 200 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "verdicts must list 1-200 items"})
			return
		}
		for _, v := range body.Verdicts {
			if v.Verdict != "approve" && v.Verdict != "reject" && v.Verdict != "defer" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "each verdict is approve, reject or defer"})
				return
			}
		}
		if err := approvalsBoardFor(d).SubmitVerdicts(c.Request.Context(), c.Param("issueId"), c.Param("interactionId"), body.Verdicts); err != nil {
			approvalsWriteErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	s.POST("/pages/approvals/issues/:issueId/comment", func(c *gin.Context) {
		var body struct {
			Message string `json:"message"`
		}
		msg := ""
		if err := c.ShouldBindJSON(&body); err == nil {
			msg = strings.TrimSpace(body.Message)
		}
		if msg == "" || len([]rune(msg)) > 20000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "message must be 1-20000 characters"})
			return
		}
		cm, err := approvalsBoardFor(d).CommentOnIssue(c.Request.Context(), c.Param("issueId"), msg)
		if err != nil {
			approvalsWriteErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "comment": cm})
	})
}
