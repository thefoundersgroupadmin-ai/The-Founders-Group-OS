package paperclip

import (
	"context"
	"strings"
	"testing"
)

const blockedRunDetail = `{"runId":"r9","status":"succeeded","runnerProfileJson":{"sessionCheckpoint":{"semanticResult":{
  "summary":"The list and draft are ready. No email was sent because the recipient address is not present.",
  "blocker":{"unblockAction":"Provide the exact recipient email address."}}}}}`

func TestCockpitThreadShowsAnswerFromRunThatPostedNoComment(t *testing.T) {
	c, b, _ := newBoard(t, "PAPERCLIP_COCKPIT_ISSUE_ID=cockpit")
	b.comments = `[{"id":"u1","body":"email me 50 prospects","authorType":"user","createdAt":"2026-11-03T03:06:45Z"}]`
	b.runs = `[{"runId":"r9","agentId":"a7","status":"succeeded","finishedAt":"2026-11-03T03:09:37Z","wakeCommentIds":["u1"]},
	  {"runId":"r1","status":"succeeded","finishedAt":"2026-11-02T21:00:00Z","wakeCommentId":"old"}]`
	b.runDetails = map[string]string{"r9": blockedRunDetail}

	for i := 0; i < 2; i++ {
		thread, err := c.CockpitThread(context.Background(), 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(thread) != 2 {
			t.Fatalf("thread = %+v", thread)
		}
		reply := thread[1]
		if reply.ID != "run-r9" || reply.AuthorType != "agent" || reply.CreatedAt != "2026-11-03T03:09:37Z" ||
			!strings.Contains(reply.Body, "recipient address is not present") ||
			!strings.Contains(reply.Body, "What I need from you: Provide the exact recipient email address.") {
			t.Fatalf("reply = %+v", reply)
		}
	}
	if n := b.hitCount("GET /api/heartbeat-runs/r9"); n != 1 {
		t.Fatalf("run detail fetched %d times, want 1 (cached)", n)
	}
}

func TestCockpitThreadWaitsWhileWokenRunIsStillGoing(t *testing.T) {
	c, b, _ := newBoard(t, "PAPERCLIP_COCKPIT_ISSUE_ID=cockpit")
	b.comments = `[{"id":"u1","body":"hi","authorType":"user","createdAt":"2026-11-03T03:06:45Z"}]`
	b.runs = `[{"runId":"r9","status":"running","wakeCommentId":"u1"}]`
	thread, err := c.CockpitThread(context.Background(), 50)
	if err != nil || len(thread) != 1 {
		t.Fatalf("thread = %+v err = %v", thread, err)
	}
	if b.hitCount("GET /api/heartbeat-runs/r9") != 0 {
		t.Fatalf("hits = %v", b.hits)
	}
}

func TestCockpitThreadSkipsRunLookupWhenAgentAlreadyReplied(t *testing.T) {
	c, b, _ := newBoard(t, "PAPERCLIP_COCKPIT_ISSUE_ID=cockpit")
	b.comments = `[{"id":"u1","body":"hi","authorType":"user","createdAt":"2026-11-03T03:06:45Z"},
	  {"id":"a1","body":"Hello","authorType":"agent","authorAgentId":"x","createdAt":"2026-11-03T03:07:00Z"}]`
	if _, err := c.CockpitThread(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if b.hitCount("GET /api/issues/cockpit/runs") != 0 {
		t.Fatalf("hits = %v", b.hits)
	}
}

func TestRunReplyTextFallsBackToFailure(t *testing.T) {
	got := RunReplyText(map[string]any{"error": "adapter crashed"}, "failed")
	if got != "The run ended without a reply (failed): adapter crashed" {
		t.Fatalf("got %q", got)
	}
	if RunReplyText(map[string]any{}, "succeeded") != "" {
		t.Fatal("a succeeded run with nothing recorded must not invent a reply")
	}
}
