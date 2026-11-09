package pods

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func TestWeekOfIsMondayInThePodsTimezone(t *testing.T) {
	// Sunday 23:30 in New York is already Monday in UTC.
	sun := time.Date(2026, 11, 16, 4, 30, 0, 0, time.UTC)
	if got := WeekOf(sun, "America/New_York").Format("2006-01-02"); got != "2026-11-09" {
		t.Fatalf("New York week: %s", got)
	}
	if got := WeekOf(sun, "Europe/London").Format("2006-01-02"); got != "2026-11-16" {
		t.Fatalf("London week: %s", got)
	}
}

func TestLastWeeksResultsAreCountedAndReviewedOncePerWeek(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.Provision(ctx, newFakeBoard(), p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET created_at = now() - interval '30 days' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBaseline(ctx, p.Slug, Baseline{MeetingsPerMonth: 4, ReplyRatePct: 1.5, Note: "founder-led outbound only"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	to := WeekOf(now, p.Timezone)
	mid := to.AddDate(0, 0, -4)
	before := to.AddDate(0, 0, -10)
	other := readyPod(t, s, "Kestrel")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Two new prospects last week (one also emailed the week before: not new), one follow-up, one reply sent.
	for i, r := range []struct {
		to, key string
		at      time.Time
	}{{"a@x.example", "s0", mid}, {"b@x.example", "s0", mid}, {"a@x.example", "s1", mid}, {"c@x.example", "s0", before}, {"c@x.example", "s1", mid}, {"a@x.example", "reply", mid}} {
		exec(`INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body, status, sent_at, sequence_key)
			VALUES ($1, $2, 'test', $3, 's', 'b', 'sent', $4, $5)`, "o"+string(rune('a'+i)), p.WorkspaceID, r.to, r.at, r.key)
	}
	exec(`INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body, status, sent_at)
		VALUES ('other', $1, 'test', 'z@x.example', 's', 'b', 'sent', $2)`, other.WorkspaceID, mid)
	exec(`INSERT INTO founderos_pod_conversations (issue_id, workspace_id, contact_id, created_at) VALUES ('i1', $1, 'c1', $2)`, p.WorkspaceID, mid)
	exec(`INSERT INTO founderos_pod_meetings (pod_id, booking_uid, attendee_email, starts_at, created_at, outcome)
		VALUES ($1, 'm1', 'a@x.example', $2, $2, 'held')`, p.ID, mid)
	exec(`INSERT INTO founderos_pod_meetings (pod_id, booking_uid, attendee_email, starts_at, created_at, status)
		VALUES ($1, 'm2', 'b@x.example', $2, $2, 'cancelled')`, p.ID, mid)

	f := &fakeTasks{}
	n, err := s.TaskWeeklyReviews(ctx, f, now)
	if err != nil || n != 1 {
		t.Fatalf("reviews tasked %d %v", n, err)
	}
	weeks, _ := s.Weeks(ctx, p.ID, 5)
	if len(weeks) != 1 {
		t.Fatalf("weeks: %+v", weeks)
	}
	m := weeks[0].M
	if m.ProspectsReached != 2 || m.EmailsSent != 4 || m.AIReplies != 1 || m.Replies != 1 || m.ReplyRatePct != 50 ||
		m.MeetingsBooked != 1 || m.MeetingsHeld != 1 || !m.DryRun {
		t.Fatalf("metrics: %+v", m)
	}
	for _, want := range []string{`"review"`, "prospects reached for the first time: 2", "4 meetings a month", "three things to work on", "Do not invent numbers"} {
		if !strings.Contains(f.bodies[0], want) {
			t.Errorf("review task lacks %q", want)
		}
	}
	if strings.Contains(f.bodies[0], "Kestrel") {
		t.Fatal("another client's data in the review")
	}
	if n, _ := s.TaskWeeklyReviews(ctx, f, now); n != 0 {
		t.Fatal("week reviewed twice")
	}
	// The review arrives and is sent once.
	exec(`UPDATE founderos_pod_weekly SET review = 'Good week: two meetings' WHERE pod_id = $1`, p.ID)
	list, err := s.ReviewsToSend(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("reviews to send: %+v %v", list, err)
	}
	body := WeeklyText(list[0], m)
	if !strings.Contains(body, "Good week") || !strings.Contains(body, "meetings booked: 1") {
		t.Fatalf("email body: %s", body)
	}
	if err := s.MarkReviewSent(ctx, list[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.ReviewsToSend(ctx); len(again) != 0 {
		t.Fatal("review sent twice")
	}
}

func TestANewPodGetsNoReviewForAWeekBeforeItExisted(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.Provision(ctx, newFakeBoard(), p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	f := &fakeTasks{}
	// The pod was created this week: nothing to review for last week.
	if n, err := s.TaskWeeklyReviews(ctx, f, time.Now()); err != nil || n != 0 {
		t.Fatalf("reviewed a week before the pod existed: %d %v", n, err)
	}
}
