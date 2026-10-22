package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/notify"
)

func fr(agent, status, at, err string) paperclip.FailoverRun {
	r := paperclip.FailoverRun{AgentID: agent, Status: status, FinishedAt: &at}
	if err != "" {
		r.Error = &err
	}
	return r
}

func TestTheConductorSaysWhenNobodyCanAnswer(t *testing.T) {
	ceo := paperclip.Agent{ID: "ceo", Name: "CEO", Status: "error"}
	other := paperclip.Agent{ID: "s", Name: "Sales Operations", Status: "idle"}
	node := "Node v22.23.3 does not satisfy Codex ACP's Node >=24.11.0 prerequisite."
	runs := []paperclip.FailoverRun{
		fr("s", "succeeded", "2026-10-22T19:00:00Z", ""),
		fr("ceo", "failed", "2026-10-22T18:42:22Z", "older"),
		fr("ceo", "failed", "2026-10-22T18:58:50Z", node),
	}
	h := healthOf([]paperclip.Agent{other, ceo}, runs)
	if h.OK || h.Agent != "CEO" || h.Problem != node || h.Since != "2026-10-22T18:58:50Z" {
		t.Fatalf("%+v", h)
	}

	// Two failures in a row read as down even before the board marks the seat.
	ceo.Status = "idle"
	if h := healthOf([]paperclip.Agent{ceo}, runs); h.OK || h.Problem != node {
		t.Fatalf("two failures: %+v", h)
	}
	// One success after a failure is healthy again.
	runs = append(runs, fr("ceo", "succeeded", "2026-10-22T20:10:00Z", ""))
	if h := healthOf([]paperclip.Agent{ceo}, runs); !h.OK || h.Problem != "" {
		t.Fatalf("recovered: %+v", h)
	}
	ceo.Status = "paused"
	if h := healthOf([]paperclip.Agent{ceo}, nil); h.OK || h.Problem != "CEO is paused, so nothing answers the Conductor." {
		t.Fatalf("paused: %+v", h)
	}
	// A board without a CEO seat says nothing.
	if h := healthOf([]paperclip.Agent{other}, runs); !h.OK {
		t.Fatalf("no seat: %+v", h)
	}
	// A healthy standby beats a broken twin.
	if h := healthOf([]paperclip.Agent{{ID: "c1", Name: "CEO", Status: "error"}, {ID: "c2", Name: "Conductor", Status: "idle"}}, nil); !h.OK || h.AgentID != "c2" {
		t.Fatalf("standby: %+v", h)
	}
}

func TestTheWatcherTellsOnceRetriesHourlyAndSaysWhenItIsBack(t *testing.T) {
	w := &conductorWatch{}
	now := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	down := conductorHealth{Agent: "CEO", AgentID: "ceo", Problem: "Node too old"}
	retries := 0
	retry := func() { retries++ }

	evs := w.step(down, now, retry)
	if len(evs) != 1 || evs[0].Kind != notify.KindHealth || evs[0].Body != "CEO: Node too old" || retries != 0 {
		t.Fatalf("first: %+v retries=%d", evs, retries)
	}
	if evs := w.step(down, now.Add(10*time.Minute), retry); len(evs) != 0 || retries != 0 {
		t.Fatalf("same problem must not nag or retry yet: %+v %d", evs, retries)
	}
	w.step(down, now.Add(61*time.Minute), retry)
	w.step(down, now.Add(70*time.Minute), retry)
	if retries != 1 {
		t.Fatalf("retries=%d, want one an hour", retries)
	}
	if evs := w.step(conductorHealth{OK: true, Agent: "CEO"}, now.Add(2*time.Hour), retry); len(evs) != 1 || evs[0].Title != "The Conductor is answering again" {
		t.Fatalf("back: %+v", evs)
	}
	if evs := w.step(conductorHealth{OK: true}, now.Add(3*time.Hour), retry); len(evs) != 0 {
		t.Fatalf("healthy stays quiet: %+v", evs)
	}
}

type repairingBoard struct {
	conductorFakeBoard
	order []string
}

func (b *repairingBoard) RepairCockpitIssue(context.Context, string) string {
	b.order = append(b.order, "repair")
	return "patched"
}

func (b *repairingBoard) PostCockpitMessage(ctx context.Context, m string) (*paperclip.Comment, error) {
	b.order = append(b.order, "post")
	return &paperclip.Comment{ID: "c1", Body: m}, nil
}

func TestSendingUnblocksTheThreadFirst(t *testing.T) {
	board := &repairingBoard{}
	r := conductorRouter(t, &ConductorDeps{Board: board})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authed(http.MethodPost, "/api/founderos/pages/conductor/chat", []byte(`{"message":"status update?"}`)))
	if w.Code != http.StatusCreated || strings.Join(board.order, ",") != "repair,post" {
		t.Fatalf("%d %s order=%v", w.Code, w.Body, board.order)
	}
}
