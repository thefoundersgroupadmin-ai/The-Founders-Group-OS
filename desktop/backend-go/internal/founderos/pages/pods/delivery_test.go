package pods

import (
	"context"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// Codex final review #6, #7: a failed send is retried (never lost), three
// failures stop, a crash mid-send is uncertain (never retried), and a pod
// paused between claim and send gets the notice back for later.
func TestNoticesAreRetriedHeldOrFlaggedNeverLostOrDoubled(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET handoff_email = 'rep@bv.example' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_meetings (pod_id, booking_uid, attendee_email, starts_at, brief)
		VALUES ($1, 'b1', 'a@x.example', $2, 'brief text')`, p.ID, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	state := func() (string, bool) {
		var d string
		var claimed bool
		_ = s.Pool.QueryRow(ctx, `SELECT brief_delivery, brief_sent_at IS NOT NULL FROM founderos_pod_meetings WHERE booking_uid = 'b1'`).Scan(&d, &claimed)
		return d, claimed
	}
	for i := 1; i <= 3; i++ {
		got, err := s.ClaimBriefs(ctx)
		if err != nil || len(got) != 1 {
			t.Fatalf("attempt %d: claimed %d %v", i, len(got), err)
		}
		if again, _ := s.ClaimBriefs(ctx); len(again) != 0 {
			t.Fatal("claimed twice while sending")
		}
		if err := s.BriefDelivered(ctx, got[0], "smtp timeout", true); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := state(); d != "failed" {
		t.Fatalf("after three failures: %s", d)
	}
	if got, _ := s.ClaimBriefs(ctx); len(got) != 0 {
		t.Fatal("a failed notice was retried a fourth time")
	}
	// A fresh brief: claimed, then the pod is paused before the send.
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET brief_delivery = '', brief_sent_at = NULL, brief_attempts = 0`); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ClaimBriefs(ctx)
	if _, err := s.SetStatus(ctx, p.Slug, StatusPaused, "holiday"); err != nil {
		t.Fatal(err)
	}
	if s.PodRunning(ctx, got[0].PodID) {
		t.Fatal("paused pod reported running")
	}
	if err := s.ReleaseBrief(ctx, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if d, claimed := state(); d != "held" || claimed {
		t.Fatalf("released: %s %v", d, claimed)
	}
	// Running again: claimed; a crash leaves it sending; after 15 minutes it is uncertain, not resent.
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ClaimBriefs(ctx); len(got) != 1 {
		t.Fatal("held notice not sent after resume")
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET brief_sent_at = now() - interval '20 minutes'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ClaimBriefs(ctx); len(got) != 0 {
		t.Fatal("an uncertain send was retried")
	}
	if d, _ := state(); d != "uncertain" {
		t.Fatalf("crashed send: %s", d)
	}
}

// Codex fix review: an ambiguous SMTP outcome is uncertain, never retried.
func TestAnAmbiguousSendIsNeverRetried(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET handoff_email = 'rep@bv.example' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_meetings (pod_id, booking_uid, attendee_email, starts_at, brief)
		VALUES ($1, 'b1', 'a@x.example', now() + interval '1 day', 'brief')`, p.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ClaimBriefs(ctx)
	if err := s.BriefDelivered(ctx, got[0], "email: delivery uncertain: EOF", false); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.ClaimBriefs(ctx); len(again) != 0 {
		t.Fatal("an uncertain brief was sent again")
	}
	var d string
	_ = s.Pool.QueryRow(ctx, `SELECT brief_delivery FROM founderos_pod_meetings`).Scan(&d)
	if d != "uncertain" {
		t.Fatalf("delivery %s", d)
	}
}
