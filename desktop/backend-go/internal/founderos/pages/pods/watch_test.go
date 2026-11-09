package pods

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

type fakeWatch struct {
	status map[string]string
	nudges []string
}

func (f *fakeWatch) TaskStatus(_ context.Context, id string) (string, error) {
	if st, ok := f.status[id]; ok {
		return st, nil
	}
	return "todo", nil
}
func (f *fakeWatch) Nudge(_ context.Context, id, msg string) error {
	f.nudges = append(f.nudges, id+": "+msg)
	return nil
}

// Codex open item: board task creation had no idempotency key, and a failed
// ownership write left an unowned task. A retry now replays the same task and
// records its owner.
func TestARetriedEnquiryReplaysTheSameTaskAndOwnsIt(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.Provision(ctx, newFakeBoard(), p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	token, _ := s.NewInboundToken(ctx, p.Slug)
	if _, err := s.Intake(ctx, token, Enquiry{Name: "Tom", Email: "tom@harbour.example", Message: "Hi"}, "ip"); err != nil {
		t.Fatal(err)
	}
	// The owner write fails once (an issue id it can't take: simulated by a
	// conflicting owner row for another pod is not possible, so break the table).
	if _, err := s.Pool.Exec(ctx, `ALTER TABLE founderos_pod_task_owner ADD CONSTRAINT no_writes CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f := &fakeTasks{}
	if n, err := s.TaskInbound(ctx, f); err == nil || n != 0 {
		t.Fatalf("an unowned task was counted: %d %v", n, err)
	}
	if _, err := s.Pool.Exec(ctx, `ALTER TABLE founderos_pod_task_owner DROP CONSTRAINT no_writes`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.TaskInbound(ctx, f); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	if len(f.titles) != 1 {
		t.Fatalf("the retry opened a second task: %v", f.titles)
	}
	var owner string
	if err := s.Pool.QueryRow(ctx, `SELECT pod_id::text FROM founderos_pod_task_owner WHERE issue_id = 'issue-b'`).Scan(&owner); err != nil || owner != p.ID {
		t.Fatalf("owner %q %v", owner, err)
	}
}

func TestASlowTaskIsNudgedOnceThenFlaggedAndAlerted(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	other := readyPod(t, s, "Kestrel")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	exec(`INSERT INTO founderos_pod_task_owner (issue_id, pod_id, kind, created_at) VALUES
		('enq-slow', $1, 'enquiry', $2), ('enq-new', $1, 'enquiry', $3), ('enq-done', $1, 'enquiry', $2),
		('rev-done', $1, 'review', $4), ('other-pod', $5, 'enquiry', $2), ('reply-done', $1, 'reply', $2)`,
		p.ID, now.Add(-25*time.Minute), now.Add(-time.Minute), now.Add(-30*time.Hour), other.ID)
	exec(`INSERT INTO founderos_pod_weekly (pod_id, week_start, review_issue_id, review) VALUES ($1, '2026-11-02', 'rev-done', 'Good week')`, p.ID)
	w := &fakeWatch{status: map[string]string{"enq-done": "done", "reply-done": "done"}}

	nudged, stuck, err := s.WatchTasks(ctx, w, now)
	if err != nil || nudged != 1 || stuck != 0 {
		t.Fatalf("first pass: nudged %d stuck %d %v", nudged, stuck, err)
	}
	if !strings.HasPrefix(w.nudges[0], "enq-slow: ") || !strings.Contains(w.nudges[0], "Brightvale") {
		t.Fatalf("nudge: %v", w.nudges)
	}
	// Nudged once only, and the settled ones are not looked at again.
	if n, _, _ := s.WatchTasks(ctx, w, now.Add(time.Minute)); n != 0 || len(w.nudges) != 1 {
		t.Fatalf("nudged twice: %v", w.nudges)
	}
	var settled int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM founderos_pod_task_owner WHERE settled_at IS NOT NULL`).Scan(&settled)
	if settled != 3 {
		t.Fatalf("settled %d, want the done enquiry and reply and the written review", settled)
	}
	// An hour on, still not done: stuck, logged, and an alert for this pod only.
	if _, stuck, err := s.WatchTasks(ctx, w, now.Add(40*time.Minute)); err != nil || stuck != 1 {
		t.Fatalf("stuck %d %v", stuck, err)
	}
	fresh, err := s.RefreshAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].Key != "task:enq-slow" || fresh[0].Slug != p.Slug || !strings.Contains(fresh[0].Title, "reply to a website enquiry") {
		t.Fatalf("alerts: %+v", fresh)
	}
	// Pushed once only.
	if again, _ := s.RefreshAlerts(ctx); len(again) != 0 {
		t.Fatalf("pushed twice: %+v", again)
	}
	// A person deals with it: the alert closes.
	if err := s.ResolveStuck(ctx, p.Slug, "enq-slow"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveStuck(ctx, other.Slug, "enq-slow"); err == nil {
		t.Fatal("another pod resolved this pod's task")
	}
	if _, err := s.RefreshAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.Alerts(ctx, p.ID); len(open) != 0 {
		t.Fatalf("still open: %+v", open)
	}
}

func TestABriefStillMissingJustBeforeTheCallIsFlaggedEarly(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_meetings (pod_id, booking_uid, attendee_email, starts_at, brief_issue_id)
		VALUES ($1, 'b1', 'a@x.example', $2, 'brief-1')`, p.ID, now.Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_task_owner (issue_id, pod_id, kind, created_at) VALUES ('brief-1', $1, 'brief', $2)`,
		p.ID, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	w := &fakeWatch{}
	if n, _, err := s.WatchTasks(ctx, w, now); err != nil || n != 1 {
		t.Fatalf("not nudged though the call is close: %d %v", n, err)
	}
	if _, stuck, err := s.WatchTasks(ctx, w, now.Add(5*time.Minute)); err != nil || stuck != 1 {
		t.Fatalf("not flagged before the call: %d %v", stuck, err)
	}
}

func TestBrokenConnectionsAndFailedNoticesRaiseAlertsThatClear(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET calendar_provider = 'calcom' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHealth(ctx, p.ID, "calendar", false, "Cal.com could not be read: 401"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_meetings (pod_id, booking_uid, attendee_email, brief_sent_at, brief_delivery)
		VALUES ($1, 'b1', 'a@x.example', now(), 'uncertain')`, p.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.RefreshAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, a := range fresh {
		keys = append(keys, a.Key)
	}
	if strings.Join(keys, ",") != "health:calendar,notice:brief" {
		t.Fatalf("alerts %v", keys)
	}
	counts, _ := s.OpenAlertCounts(ctx)
	if counts[p.Slug] != 2 {
		t.Fatalf("counts %v", counts)
	}
	// The calendar works again: that alert closes; if it breaks again it is pushed again.
	_ = s.SetHealth(ctx, p.ID, "calendar", true, "")
	if _, err := s.RefreshAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.Alerts(ctx, p.ID); len(open) != 1 || open[0].Key != "notice:brief" {
		t.Fatalf("open %+v", open)
	}
	_ = s.SetHealth(ctx, p.ID, "calendar", false, "401 again")
	if again, _ := s.RefreshAlerts(ctx); len(again) != 1 || again[0].Key != "health:calendar" {
		t.Fatalf("re-broken calendar not pushed again: %+v", again)
	}
	// Handed over: everything closes.
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET status = 'handed_over' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.Alerts(ctx, p.ID); len(open) != 0 {
		t.Fatalf("open after hand-over %+v", open)
	}
}
