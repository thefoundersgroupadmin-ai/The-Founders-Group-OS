package paperclip

import (
	"context"
	"fmt"
	"strings"
)

// A cockpit run can finish without posting a comment: native runs that end
// blocked (for example asking the operator for missing input) record their
// answer only as the run's semantic result. The Conductor thread is built from
// comments, so the operator's message then spins forever. runReply recovers
// that answer from the finished run that the operator's comment woke.

var terminalRunStatuses = map[string]bool{
	"succeeded": true, "failed": true, "timed_out": true, "cancelled": true, "interrupted": true,
}

// withRunReply appends the woken run's answer when the thread ends on an
// operator message. Best effort: any board error leaves the thread unchanged.
func (c *Connector) withRunReply(ctx context.Context, issueID string, thread []Comment) []Comment {
	if len(thread) == 0 {
		return thread
	}
	last := thread[len(thread)-1]
	if last.AuthorType == "agent" {
		return thread
	}
	if cached, ok := c.runReplies.Load(last.ID); ok {
		return append(thread, cached.(Comment))
	}
	reply, ok := c.runReply(ctx, issueID, last)
	if !ok {
		return thread
	}
	// Finished runs never change their answer, so look each one up once.
	c.runReplies.Store(last.ID, reply)
	return append(thread, reply)
}

func (c *Connector) runReply(ctx context.Context, issueID string, operator Comment) (Comment, bool) {
	body, err := c.get(ctx, fmt.Sprintf("/api/issues/%s/runs", issueID), issueTimeout)
	if err != nil {
		return Comment{}, false
	}
	for _, raw := range listFrom(body, "runs") {
		run := obj(raw)
		if !runWokenBy(run, operator) {
			continue
		}
		status, _ := run["status"].(string)
		finishedAt := str(run["finishedAt"])
		runID := str(run["runId"])
		if !terminalRunStatuses[status] || finishedAt == nil || runID == nil {
			return Comment{}, false // still running: the normal reply may yet land
		}
		detail, err := c.get(ctx, "/api/heartbeat-runs/"+*runID, issueTimeout)
		if err != nil {
			return Comment{}, false
		}
		text := RunReplyText(obj(detail), status)
		if text == "" {
			return Comment{}, false
		}
		agentID := str(run["agentId"])
		return Comment{ID: "run-" + *runID, Body: text, AuthorType: "agent", AuthorAgentID: agentID, CreatedAt: *finishedAt}, true
	}
	return Comment{}, false
}

// runWokenBy matches a run to the operator comment that woke it.
func runWokenBy(run map[string]any, operator Comment) bool {
	if operator.ID == "" {
		return false
	}
	for _, key := range []string{"wakeCommentId", "contextCommentId"} {
		if s, _ := run[key].(string); s == operator.ID {
			return true
		}
	}
	if ids, ok := run["wakeCommentIds"].([]any); ok {
		for _, id := range ids {
			if s, _ := id.(string); s == operator.ID {
				return true
			}
		}
	}
	return false
}

// RunReplyText is the operator-facing answer a finished run recorded: its
// semantic summary plus any unblock request, else a plain failure line.
func RunReplyText(detail map[string]any, status string) string {
	semantic := obj(obj(obj(detail["runnerProfileJson"])["sessionCheckpoint"])["semanticResult"])
	summary := strings.TrimSpace(stringOf(semantic["summary"]))
	ask := strings.TrimSpace(stringOf(obj(semantic["blocker"])["unblockAction"]))
	if summary != "" {
		if ask != "" && !strings.Contains(summary, ask) {
			return summary + "\n\nWhat I need from you: " + ask
		}
		return summary
	}
	if ask != "" {
		return "What I need from you: " + ask
	}
	if snippet := strings.TrimSpace(stringOf(detail["lastAssistantSnippet"])); snippet != "" && status == "succeeded" {
		return snippet
	}
	if status != "succeeded" {
		if e := strings.TrimSpace(stringOf(detail["error"])); e != "" {
			return "The run ended without a reply (" + status + "): " + e
		}
		return "The run ended without a reply (" + status + "). Send the message again to retry."
	}
	return ""
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
