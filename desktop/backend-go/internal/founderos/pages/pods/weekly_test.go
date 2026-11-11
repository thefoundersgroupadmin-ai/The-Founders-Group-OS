package pods

import (
	"context"
	"errors"
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
	to := WeekOf(now.Add(-reviewGrace), p.Timezone)
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
	exec(`INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email)
		VALUES ('c1', $1, 'Ana', 'engaged', 'client', now(), 'A@x.example')`, p.WorkspaceID)
	exec(`INSERT INTO founderos_pod_conversations (issue_id, workspace_id, contact_id, created_at) VALUES ('i1', $1, 'c1', $2)`, p.WorkspaceID, mid)
	// The reply itself, as the reply checker records it (an automatic reply doesn't count).
	exec(`INSERT INTO founderos_pod_reply_messages (pod_id, message_id, sender, contact_id, received_at, routed) VALUES ($1, 'm1', 'a@acme.example', 'c1', $2, 'ai'),
		($1, 'm2', 'ooo@acme.example', 'c1', $2, 'auto')`, p.ID, mid)
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
	if m.ProspectsReached != 2 || m.EmailsSent != 4 || m.AIReplies != 1 || m.Replies != 1 ||
		m.CohortSize != 3 || m.CohortReplied != 1 || m.ReplyRatePct != 33.3 ||
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
	list, err := s.ClaimReviews(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("reviews to send: %+v %v", list, err)
	}
	body := WeeklyText(list[0], m)
	if !strings.Contains(body, "Good week") || !strings.Contains(body, "meetings booked: 1") {
		t.Fatalf("email body: %s", body)
	}
	if err := s.ReviewDelivered(ctx, list[0], "", false); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.ClaimReviews(ctx); len(again) != 0 {
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

// Codex M6 review #5, #6: late data lands in its week, and a week whose review
// task failed is retried.
func TestLateDataIsRecountedAndAFailedReviewTaskIsRetried(t *testing.T) {
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
	now := time.Now()
	to := WeekOf(now.Add(-reviewGrace), p.Timezone)
	fail := &failingTasks{}
	if _, err := s.TaskWeeklyReviews(ctx, fail, now); err == nil {
		t.Fatal("a failed review task was not reported")
	}
	// A Sunday call is marked held after the week was first counted.
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_meetings (pod_id, booking_uid, attendee_email, starts_at, created_at, outcome)
		VALUES ($1, 'late', 'a@x.example', $2, $2, 'held')`, p.ID, to.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f := &fakeTasks{}
	if n, err := s.TaskWeeklyReviews(ctx, f, now); err != nil || n != 1 {
		t.Fatalf("retry tasked %d %v", n, err)
	}
	weeks, _ := s.Weeks(ctx, p.ID, 1)
	if weeks[0].M.MeetingsHeld != 1 || !strings.Contains(f.bodies[0], "held: 1") {
		t.Fatalf("late call not counted: %+v", weeks[0].M)
	}
}

type failingTasks struct{}

func (failingTasks) CreateTask(context.Context, string, string, string, string, string) (string, error) {
	return "", errors.New("board down")
}

// Codex final review #9: a review whose numbers changed after it was written is flagged.
func TestAReviewIsFlaggedWhenItsNumbersChangeLater(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_weekly (pod_id, week_start, metrics, review, review_metrics)
		VALUES ($1, '2026-11-02', '{"meetingsHeld":1}', 'One call held', '{"meetingsHeld":1}'),
		       ($1, '2026-10-26', '{"meetingsHeld":2}', 'One call held', '{"meetingsHeld":1}')`, p.ID); err != nil {
		t.Fatal(err)
	}
	weeks, err := s.Weeks(ctx, p.ID, 5)
	if err != nil || len(weeks) != 2 || weeks[0].Corrected || !weeks[1].Corrected {
		t.Fatalf("corrected flags: %+v %v", weeks, err)
	}
}

// Codex: subject lines are credited by the reply's thread, not by address.
func TestSubjectLinesAreCreditedOnlyWithTheEmailARepliedTo(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	// Two emails to the same person; they answered the second.
	exec(`INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body, status, sent_at, sequence_key) VALUES
		('o1', $1, 'test', 'a@x.example', 'Three warehouses', 'b', 'sent', $2, 's0'),
		('o2', $1, 'test', 'a@x.example', 'Quick follow-up', 'b', 'sent', $3, 's1')`, p.WorkspaceID, now.Add(-72*time.Hour), now.Add(-24*time.Hour))
	exec(`INSERT INTO founderos_pod_reply_messages (pod_id, message_id, sender, outbox_id, threaded) VALUES ($1, '<m1>', 'a@x.example', 'o2', true)`, p.ID)
	ev := s.reviewEvidence(ctx, p, now.AddDate(0, 0, -7), now.Add(time.Hour))
	if !strings.Contains(ev, `"Quick follow-up": sent 1, replied 1`) || !strings.Contains(ev, `"Three warehouses": sent 1, replied 0`) {
		t.Fatalf("evidence:\n%s", ev)
	}
}
