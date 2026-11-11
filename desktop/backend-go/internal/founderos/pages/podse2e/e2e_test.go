// Package podse2e walks one fictional client through the whole AI SDR
// service, signature to hand-off (docs/tfg/CLIENT-PODS.md, M8): setup,
// documents and approvals, provisioning, the test run, the portal, going
// live, replies and website enquiries, meetings with briefs, recorded calls
// with coaching, the weekly review and results, the hand-off bundle, and the
// hand-over that stops every send. External services are fakes; the database,
// the pod store, the send gate and the portal sync are the real ones.
package podse2e

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
	"github.com/rhl/businessos-backend/internal/founderos/pages/portalsync"
)

// board is the task board (Paperclip): it records projects, agents, routines and tasks.
type board struct {
	n      int
	tasks  []string
	agents map[string]string
}

func (b *board) id(p string) string { b.n++; return fmt.Sprintf("%s-%d", p, b.n) }
func (b *board) EnsureProject(_ context.Context, key, name, _ string) (string, error) {
	return "proj-" + key, nil
}
func (b *board) EnsureAgent(_ context.Context, workerID, name, instructions string) (string, error) {
	if b.agents == nil {
		b.agents = map[string]string{}
	}
	b.agents[workerID] = instructions
	return "agent-" + workerID, nil
}
func (b *board) PutInstructions(context.Context, string, string) error { return nil }
func (b *board) CreateRoutine(_ context.Context, r pods.BoardRoutine) (string, error) {
	return b.id("routine"), nil
}
func (b *board) UpdateRoutine(context.Context, string, pods.BoardRoutine) error { return nil }
func (b *board) AddScheduleTrigger(context.Context, string, string, string) (string, error) {
	return b.id("trigger"), nil
}
func (b *board) UpdateScheduleTrigger(context.Context, string, string, string) error { return nil }
func (b *board) CreateTask(_ context.Context, key, project, agent, title, body string) (string, error) {
	b.tasks = append(b.tasks, title+"\n"+body)
	return b.id("issue"), nil
}

// portal is the client's portal (Supabase), as the sync sees it.
type portal struct {
	pod       portalsync.Pod
	approvals map[string]*portalsync.Approval
	activity  []portalsync.Activity
	requests  []*portalsync.Request
	meetings  map[int64]portalsync.Meeting
	weekly    map[string]portalsync.Weekly
	n         int
}

func (p *portal) UpsertPod(_ context.Context, x portalsync.Pod) error { p.pod = x; return nil }
func (p *portal) Approvals(_ context.Context, _, _ string) ([]portalsync.Approval, error) {
	var out []portalsync.Approval
	for _, a := range p.approvals {
		out = append(out, *a)
	}
	return out, nil
}
func (p *portal) InsertApproval(_ context.Context, a portalsync.Approval) error {
	p.n++
	a.ID = fmt.Sprintf("ap-%d", p.n)
	p.approvals[a.ID] = &a
	return nil
}
func (p *portal) UpdateApproval(_ context.Context, id string, f map[string]any) error {
	a := p.approvals[id]
	for k, v := range f {
		switch k {
		case "status":
			a.Status = v.(string)
		case "content_hash":
			a.ContentHash = v.(string)
		case "title":
			a.Title = v.(string)
		case "pulled_at":
			a.Pulled = v != nil
		}
	}
	return nil
}
func (p *portal) InsertActivity(_ context.Context, rows []portalsync.Activity) error {
	p.activity = append(p.activity, rows...)
	return nil
}
func (p *portal) OpenRequests(_ context.Context, _ string) ([]portalsync.Request, error) {
	var out []portalsync.Request
	for _, r := range p.requests {
		if r.Outcome == "" {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (p *portal) CloseRequest(_ context.Context, id, outcome string) error {
	for _, r := range p.requests {
		if r.ID == id {
			r.Outcome = outcome
		}
	}
	return nil
}
func (p *portal) UpsertMeetings(_ context.Context, rows []portalsync.Meeting) error {
	for _, m := range rows {
		p.meetings[m.OSID] = m
	}
	return nil
}
func (p *portal) UpsertCampaigns(context.Context, []portalsync.Campaign) error { return nil }
func (p *portal) UpsertProspects(context.Context, []portalsync.Prospect) error { return nil }
func (p *portal) NewSupportMessages(context.Context, string) ([]portalsync.SupportMessage, error) {
	return nil, nil
}
func (p *portal) MarkSupportPulled(context.Context, string) error    { return nil }
func (p *portal) ClientName(context.Context, string) (string, error) { return "Client", nil }
func (p *portal) ClearPod(context.Context, string, string) error     { return nil }
func (p *portal) UpsertWeekly(_ context.Context, rows []portalsync.Weekly) error {
	for _, w := range rows {
		p.weekly[w.WeekStart] = w
	}
	return nil
}

func TestAFictionalClientFromSignatureToHandoff(t *testing.T) {
	pool := catalogdb.TestDB(t)
	s := &pods.Store{Pool: pool}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, q, args...)
		must(err)
	}

	// 1. Signature: the client gets a pod and a workspace of their own.
	p, err := s.Create(ctx, pods.NewPod{ClientName: "Northwind Demo", OwnerID: "owner", Package: "core", Reps: 4})
	must(err)
	other, err := s.Create(ctx, pods.NewPod{ClientName: "Kestrel Other", OwnerID: "owner", Package: "core", Reps: 2})
	must(err)
	must(s.SetDoc(ctx, other.Slug, "icp", "KESTREL-ONLY"))

	// 2. Onboarding: sender, sending rules, documents, client sign-off, portal, baseline.
	must(s.SetSender(ctx, p.Slug, pods.Sender{Name: "Sam Reed", Email: "sam@northwind-mail.example", Domain: "northwind-mail.example", AccountID: "pod:" + p.Slug}))
	must(s.UpdateSettings(ctx, p.Slug, pods.Settings{ApprovalPolicy: "auto", FirstEmailDailyCap: 20, FollowUpDailyCap: 40,
		Timezone: "UTC", SendWindowStart: 0, SendWindowEnd: 24, SendWeekends: true, DryRunInbox: "operator@tfg.example",
		HandoffEmail: "rep@northwind.example"}))
	for k, body := range map[string]string{"icp": "Warehouse operators, 50-500 staff", "voice": "Plain and warm",
		"product": "One live stock count", "targets": "acme.example", "exclusions": "Oakfield", "playbook": "Ask who signs off",
		"qualification": "Budget, timing, decision maker"} {
		must(s.SetDoc(ctx, p.Slug, k, body))
		must(s.ApproveDoc(ctx, p.Slug, k, "Priya (client) by email"))
	}
	must(s.LinkPortal(ctx, p.Slug, "7f1c2a9e-0d3b-4c6e-9a1f-2b8d4e6f0a11"))
	must(s.SetBaseline(ctx, p.Slug, pods.Baseline{MeetingsPerMonth: 2, ReplyRatePct: 1, Note: "founder-led outbound"}))

	// 3. Its own AI SDR on the task board, briefed only with its documents.
	b := &board{}
	bind, err := s.Provision(ctx, b, p.Slug)
	must(err)
	for _, ins := range b.agents {
		if strings.Contains(ins, "KESTREL") {
			t.Fatal("another client's document reached this client's agent")
		}
	}

	// 4. Test run: emails go to TFG's test inbox, never the prospect.
	p, _ = s.Get(ctx, p.Slug)
	_, err = s.SetStatus(ctx, p.Slug, pods.StatusDryRun, "")
	must(err)
	var sent []outbox.Item
	send := func(_ context.Context, it outbox.Item) error { sent = append(sent, it); return nil }
	draft := func(id, to string) {
		exec(`INSERT INTO founderos_outbox (id, workspace_id, source, to_address, to_name, company, subject, body)
			VALUES ($1, $2, 'paperclip', $3, 'Ana Ops', 'Acme', 'One stock count', 'Hi Ana, body. Sam Reed. If this isn''t relevant, reply "no thanks" and I won''t follow up.')`, id, p.WorkspaceID, to)
	}
	reviewed := outbox.Content{Subject: "One stock count", Body: `Hi Ana, body. Sam Reed. If this isn't relevant, reply "no thanks" and I won't follow up.`}
	draft("t-1", "ana@acme.example")
	it, err := outbox.Approve(ctx, pool, p.Slug, "t-1", reviewed, send)
	must(err)
	if it.Status != "simulated" || len(sent) != 1 || sent[0].To != "operator@tfg.example" {
		t.Fatalf("test run: %s %+v", it.Status, sent)
	}

	// 5. The portal shows it; the client asks to pause, then resume.
	pt := &portal{approvals: map[string]*portalsync.Approval{}, meetings: map[int64]portalsync.Meeting{}, weekly: map[string]portalsync.Weekly{}}
	_, err = portalsync.Sync(ctx, s, pt)
	must(err)
	if pt.pod.Status != pods.StatusDryRun || pt.pod.Baseline == nil {
		t.Fatalf("portal pod: %+v", pt.pod)
	}
	pt.requests = append(pt.requests, &portalsync.Request{ID: "rq-1", ClientID: "7f1c2a9e-0d3b-4c6e-9a1f-2b8d4e6f0a11", Kind: "pause", Reason: "trade show week", RequestedByName: "Priya"})
	_, err = portalsync.Sync(ctx, s, pt)
	must(err)
	if p, _ = s.Get(ctx, p.Slug); p.Status != pods.StatusPaused {
		t.Fatalf("pause from the portal: %s", p.Status)
	}
	draft("t-2", "tom@acme.example")
	if _, err := outbox.Approve(ctx, pool, p.Slug, "t-2", reviewed, send); !errors.Is(err, outbox.ErrHeld) {
		t.Fatalf("paused pod sent: %v", err)
	}
	_, err = s.SetStatus(ctx, p.Slug, pods.StatusDryRun, "")
	must(err)

	// 6. Live: real email from the client's own mailbox. This client books through the rep's own
	// calendar, so the booking-calendar requirement is waived with that reason.
	must(s.WaiveReadiness(ctx, p.Slug, "calendar", "the client's rep books meetings from their own calendar", "Nader", true))
	_, err = s.SetStatus(ctx, p.Slug, pods.StatusLive, "")
	must(err)
	draft("l-1", "ben@acme.example")
	it, err = outbox.Approve(ctx, pool, p.Slug, "l-1", reviewed, send)
	must(err)
	last := sent[len(sent)-1]
	if it.Status != "sent" || last.To != "ben@acme.example" || last.AccountID == nil || *last.AccountID != "pod:"+p.Slug {
		t.Fatalf("live send: %s %+v", it.Status, last)
	}

	// 7. Ben replies (the reply importer records the conversation) and a website enquiry arrives.
	exec(`INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email, company)
		VALUES ('c-ben', $1, 'Ben', 'engaged', 'client', now(), 'ben@acme.example', 'Acme')`, p.WorkspaceID)
	exec(`INSERT INTO founderos_pod_conversations (issue_id, workspace_id, contact_id) VALUES ('issue-reply', $1, 'c-ben')`, p.WorkspaceID)
	token, err := s.NewInboundToken(ctx, p.Slug)
	must(err)
	_, err = s.Intake(ctx, token, pods.Enquiry{Name: "Cara", Email: "cara@globex.example", Company: "Globex", Message: "Pricing for 3 sites?"}, "web")
	must(err)
	if k, err := s.TaskInbound(ctx, b); err != nil || k != 1 {
		t.Fatalf("enquiry tasked %d %v", k, err)
	}

	// 8. Ben books a call: it becomes a meeting, the AI stops replying, and a brief goes to the rep.
	start := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Minute)
	_, err = s.RecordMeetings(ctx, p.Slug, []pods.Meeting{{UID: "bk-1", Title: "Discovery", Start: start.Format(time.RFC3339), AttendeeEmail: "ben@acme.example", AttendeeName: "Ben"}})
	must(err)
	var taken bool
	must(pool.QueryRow(ctx, `SELECT taken_over_at IS NOT NULL FROM founderos_pod_conversations WHERE issue_id = 'issue-reply'`).Scan(&taken))
	if !taken {
		t.Fatal("a booking did not hand the conversation to a person")
	}
	if k, err := s.TaskBriefs(ctx, b); err != nil || k != 1 {
		t.Fatalf("brief tasked %d %v", k, err)
	}
	exec(`UPDATE founderos_pod_meetings SET brief = 'Ben runs ops at Acme' WHERE booking_uid = 'bk-1'`)
	if briefs, err := s.ClaimBriefs(ctx); err != nil || len(briefs) != 1 || briefs[0].To != "rep@northwind.example" {
		t.Fatalf("brief to rep: %+v %v", briefs, err)
	}

	// 9. The call happens and is recorded; coaching notes follow.
	exec(`UPDATE founderos_pod_meetings SET starts_at = now() - interval '1 hour' WHERE booking_uid = 'bk-1'`)
	var at time.Time
	must(pool.QueryRow(ctx, `SELECT starts_at FROM founderos_pod_meetings WHERE booking_uid = 'bk-1'`).Scan(&at))
	n, err := s.AttachRecordings(ctx, p.Slug, []pods.Recording{{Ref: "fathom:1", Start: at.Add(3 * time.Minute), Invitees: []string{"ben@acme.example"}, Transcript: "Ben: we lose a day a week to counts"}})
	if err != nil || n != 1 {
		t.Fatalf("recording matched %d %v", n, err)
	}
	if k, err := s.TaskCoaching(ctx, b); err != nil || k != 1 {
		t.Fatalf("coaching tasked %d %v", k, err)
	}
	exec(`UPDATE founderos_pod_meetings SET coaching = 'Ask about budget earlier' WHERE booking_uid = 'bk-1'`)
	if c, err := s.ClaimCoaching(ctx); err != nil || len(c) != 1 {
		t.Fatalf("coaching to rep: %+v %v", c, err)
	}

	// 10. The weekly review counts the week and the AI SDR writes it up.
	exec(`UPDATE founderos_client_pods SET created_at = now() - interval '30 days' WHERE id = $1`, p.ID)
	nextWeek := pods.WeekOf(time.Now(), "UTC").AddDate(0, 0, 7).Add(12 * time.Hour)
	if k, err := s.TaskWeeklyReviews(ctx, b, nextWeek); err != nil || k < 1 {
		t.Fatalf("weekly review tasked %d %v", k, err)
	}
	weeks, err := s.Weeks(ctx, p.ID, 1)
	must(err)
	m := weeks[0].M
	if m.EmailsSent != 1 || m.Replies != 1 || m.Enquiries != 1 || m.MeetingsBooked != 1 || m.MeetingsHeld != 1 || m.CohortSize != 1 || m.ReplyRatePct != 100 {
		t.Fatalf("week counted: %+v", m)
	}
	exec(`UPDATE founderos_pod_weekly SET review = 'Strong first week: one call booked and held' WHERE pod_id = $1`, p.ID)
	if r, err := s.ClaimReviews(ctx); err != nil || len(r) != 1 {
		t.Fatalf("weekly review to client: %+v %v", r, err)
	}

	// 11. The portal shows the brief, coaching, results and review.
	_, err = portalsync.Sync(ctx, s, pt)
	must(err)
	var pm portalsync.Meeting
	for _, x := range pt.meetings {
		pm = x
	}
	if pm.Brief != "Ben runs ops at Acme" || pm.Coaching != "Ask about budget earlier" || pm.Outcome != "held" {
		t.Fatalf("portal meeting: %+v", pm)
	}
	if len(pt.weekly) != 1 {
		t.Fatalf("portal weekly: %+v", pt.weekly)
	}

	// 12. Hand-off: the bundle holds everything the client owns, and nothing of anyone else's.
	raw, err := s.Bundle(ctx, p.Slug)
	must(err)
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	must(err)
	var all strings.Builder
	names := map[string]bool{}
	for _, f := range z.File {
		names[f.Name] = true
		r, _ := f.Open()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		all.WriteString(buf.String())
	}
	for _, want := range []string{"README.md", "ai-sdr/instructions.md", "emails-sent.csv", "pipeline.csv", "meetings.csv", "weekly-results.csv", "documents/playbook.md"} {
		if !names[want] {
			t.Errorf("bundle lacks %s", want)
		}
	}
	if !strings.Contains(all.String(), "ben@acme.example") || !strings.Contains(all.String(), "Strong first week") || strings.Contains(all.String(), "KESTREL") {
		t.Fatal("bundle content wrong")
	}
	_, err = s.SetStatus(ctx, p.Slug, pods.StatusHandedOver, "")
	must(err)
	draft("h-1", "dan@acme.example")
	if _, err := outbox.Approve(ctx, pool, p.Slug, "h-1", reviewed, send); !errors.Is(err, outbox.ErrHeld) {
		t.Fatalf("a handed-over pod still sends: %v", err)
	}
	_ = bind
}
