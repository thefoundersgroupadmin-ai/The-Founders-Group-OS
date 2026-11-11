package portalsync

import (
	"context"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

type fakePortal struct {
	meetings  map[int64]Meeting
	weekly    map[string]Weekly
	pods      map[string]Pod
	approvals map[string]*Approval // id → row
	activity  []Activity
	requests  []*Request
	campaigns []Campaign
	prospects []Prospect
	support   []*SupportMessage
	pulled    map[string]bool
	cleared   []string
	n         int
}

func newFake() *fakePortal {
	return &fakePortal{pods: map[string]Pod{}, approvals: map[string]*Approval{}}
}

func (f *fakePortal) UpsertPod(_ context.Context, p Pod) error { f.pods[p.ClientID] = p; return nil }
func (f *fakePortal) UpsertCampaigns(_ context.Context, rows []Campaign) error {
	f.campaigns = append(f.campaigns, rows...)
	return nil
}
func (f *fakePortal) UpsertProspects(_ context.Context, rows []Prospect) error {
	f.prospects = append(f.prospects, rows...)
	return nil
}
func (f *fakePortal) NewSupportMessages(_ context.Context, client string) ([]SupportMessage, error) {
	var out []SupportMessage
	for _, m := range f.support {
		if m.ClientID == client && !f.pulled[m.ID] {
			out = append(out, *m)
		}
	}
	return out, nil
}
func (f *fakePortal) MarkSupportPulled(_ context.Context, id string) error {
	if f.pulled == nil {
		f.pulled = map[string]bool{}
	}
	f.pulled[id] = true
	return nil
}
func (f *fakePortal) ClientName(_ context.Context, id string) (string, error) {
	if id == portalClient || id == otherClient {
		return "Brightvale Ltd", nil
	}
	return "", ErrNoSuchClient
}
func (f *fakePortal) ClearPod(_ context.Context, client, slug string) error {
	f.cleared = append(f.cleared, client+"/"+slug)
	return nil
}
func (f *fakePortal) Approvals(_ context.Context, client, slug string) ([]Approval, error) {
	var out []Approval
	for _, a := range f.approvals {
		if a.ClientID == client && a.PodSlug == slug {
			out = append(out, *a)
		}
	}
	return out, nil
}
func (f *fakePortal) InsertApproval(_ context.Context, a Approval) error {
	f.n++
	a.ID = "pa-" + string(rune('a'+f.n))
	f.approvals[a.ID] = &a
	return nil
}
func (f *fakePortal) UpdateApproval(_ context.Context, id string, fields map[string]any) error {
	a := f.approvals[id]
	for k, v := range fields {
		switch k {
		case "status":
			a.Status = v.(string)
		case "content_hash":
			a.ContentHash = v.(string)
		case "pulled_at":
			a.Pulled = v != nil
		case "decided_at":
			if v == nil {
				a.DecidedAt = ""
			}
		}
	}
	return nil
}
func (f *fakePortal) InsertActivity(_ context.Context, rows []Activity) error {
	f.activity = append(f.activity, rows...)
	return nil
}
func (f *fakePortal) OpenRequests(_ context.Context, client string) ([]Request, error) {
	var out []Request
	for _, r := range f.requests {
		if r.ClientID == client && r.Outcome == "" {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *fakePortal) CloseRequest(_ context.Context, id, outcome string) error {
	for _, r := range f.requests {
		if r.ID == id {
			r.Outcome = outcome
		}
	}
	return nil
}

const portalClient = "8f14e45f-ceea-467a-9a36-dedd4bea2543"
const otherClient = "1c9a1d2e-0000-4000-8000-000000000001"

func setup(t *testing.T) (*pods.Store, pods.Pod) {
	t.Helper()
	s := &pods.Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p, err := s.Create(ctx, pods.NewPod{ClientName: "Brightvale", OwnerID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetSender(ctx, p.Slug, pods.Sender{Name: "Sam", Email: "sam@bv.example", AccountID: "pod:" + p.Slug})
	_ = s.LinkPortal(ctx, p.Slug, portalClient)
	_ = s.SetDoc(ctx, p.Slug, "icp", "Warehouse operators")
	return s, p
}

func TestSyncPushesStateApprovalsAndActivityAndAppliesCurrentDecisions(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	f := newFake()
	if _, err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	if got := f.pods[portalClient]; got.PodSlug != p.Slug || got.Status != "setup" || !strings.Contains(got.Sender, "sam@bv.example") {
		t.Fatalf("pod row %+v", got)
	}
	if len(f.approvals) != 1 || len(f.activity) == 0 {
		t.Fatalf("approvals %d activity %d", len(f.approvals), len(f.activity))
	}
	var a *Approval
	for _, v := range f.approvals {
		a = v
	}
	if a.OSKey != "doc:icp" || a.Status != "pending" || a.ContentHash == "" {
		t.Fatalf("approval %+v", a)
	}
	// Running again pushes nothing twice.
	before := len(f.activity)
	_, _ = Sync(ctx, s, f)
	if len(f.approvals) != 1 || len(f.activity) != before {
		t.Fatalf("duplicates: %d approvals, %d→%d activity", len(f.approvals), before, len(f.activity))
	}
	// The client approves in the portal; the OS applies it once.
	a.Status, a.DecidedAt, a.DecidedByName = "approved", "2026-11-09T10:00:00Z", "Priya Shah"
	if _, err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Doc(ctx, p.Slug, "icp"); d.ApprovedAt == nil {
		t.Fatal("portal approval not applied")
	}
	if !a.Pulled {
		t.Fatal("decision not marked pulled")
	}
}

func TestADecisionOnOldWordingIsSentBackToPending(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	f := newFake()
	_, _ = Sync(ctx, s, f)
	var a *Approval
	for _, v := range f.approvals {
		a = v
	}
	_ = s.SetDoc(ctx, p.Slug, "icp", "Warehouse operators, 50-500 staff") // edited after the client looked
	a.Status, a.DecidedAt, a.DecidedByName = "approved", "2026-11-09T10:00:00Z", "Priya Shah"
	if _, err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Doc(ctx, p.Slug, "icp"); d.ApprovedAt != nil {
		t.Fatal("approval of old wording applied to new wording")
	}
	if a.Status != "pending" {
		t.Fatalf("stale decision not reset: %s", a.Status)
	}
}

func TestClientPauseRequestsPauseThePod(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	if _, err := s.SetStatus(ctx, p.Slug, pods.StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	f.requests = append(f.requests, &Request{ID: "rq-1", ClientID: portalClient, Kind: "pause", Reason: "holiday", RequestedByName: "Priya"})
	if _, err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, p.Slug)
	if got.Status != pods.StatusPaused || !strings.Contains(got.PausedReason, "holiday") || f.requests[0].Outcome == "" {
		t.Fatalf("pause not applied: %s %q %q", got.Status, got.PausedReason, f.requests[0].Outcome)
	}
}

func TestMeetingsAndBriefsReachThePortal(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	if _, err := s.RecordMeetings(ctx, p.Slug, []pods.Meeting{{UID: "b-1", Title: "Discovery call", Start: "2030-01-07T10:00:00Z",
		AttendeeEmail: "priya@bv.example", AttendeeName: "Priya Shah"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET brief = 'Priya runs ops.'`); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	if _, err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	if len(f.meetings) != 1 {
		t.Fatalf("meetings %d", len(f.meetings))
	}
	for _, m := range f.meetings {
		if m.Attendee != "Priya Shah" || m.Brief != "Priya runs ops." || m.StartsAt == nil || *m.StartsAt != "2030-01-07T10:00:00Z" {
			t.Fatalf("meeting %+v", m)
		}
	}
}

func (f *fakePortal) UpsertMeetings(_ context.Context, rows []Meeting) error {
	if f.meetings == nil {
		f.meetings = map[int64]Meeting{}
	}
	for _, m := range rows {
		f.meetings[m.OSID] = m
	}
	return nil
}

func (f *fakePortal) UpsertWeekly(_ context.Context, rows []Weekly) error {
	if f.weekly == nil {
		f.weekly = map[string]Weekly{}
	}
	for _, w := range rows {
		f.weekly[w.WeekStart] = w
	}
	return nil
}

// M6: coaching notes, weekly results and the baseline reach the portal.
func TestCoachingWeeklyResultsAndBaselineReachThePortal(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	if _, err := s.RecordMeetings(ctx, p.Slug, []pods.Meeting{{UID: "b-1", Title: "Discovery call", Start: "2026-01-07T10:00:00Z",
		AttendeeEmail: "priya@bv.example", AttendeeName: "Priya Shah"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET outcome = 'held', coaching = 'Ask about budget earlier'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_weekly (pod_id, week_start, metrics, review)
		VALUES ($1, '2026-11-02', '{"week":"2026-11-02","meetingsBooked":3}', 'Good week')`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBaseline(ctx, p.Slug, pods.Baseline{MeetingsPerMonth: 4, ReplyRatePct: 1.5}); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	if _, err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.meetings {
		if m.Outcome != "held" || m.Coaching != "Ask about budget earlier" {
			t.Fatalf("meeting %+v", m)
		}
	}
	w, ok := f.weekly["2026-11-02"]
	if !ok || w.Review != "Good week" || w.Metrics.MeetingsBooked != 3 {
		t.Fatalf("weekly %+v", f.weekly)
	}
	for _, pp := range f.pods {
		if b, ok := pp.Baseline.(pods.Baseline); !ok || b.MeetingsPerMonth != 4 {
			t.Fatalf("baseline %+v", pp.Baseline)
		}
	}
}


// Several pods per client, client-wide pause, client messages, portal health, campaigns and pipeline.
func TestTwoPodsOneClientPauseAllMessagesAndHealth(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	q, err := s.Create(ctx, pods.NewPod{ClientName: "Brightvale", Label: "UK", OwnerID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.LinkPortal(ctx, q.Slug, portalClient)
	_ = s.SetDoc(ctx, q.Slug, "icp", "UK warehouse operators")
	for _, x := range []pods.Pod{p, q} {
		if _, err := s.SetStatus(ctx, x.Slug, pods.StatusDryRun, ""); err != nil {
			t.Fatal(err)
		}
	}
	f := newFake()
	f.requests = append(f.requests, &Request{ID: "rq-all", ClientID: portalClient, Kind: "pause", Reason: "rebrand"})
	f.support = append(f.support, &SupportMessage{ID: "m1", ClientID: portalClient, Topic: "change", Body: "Can we add a pod?"})
	res, err := Sync(ctx, s, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []pods.Pod{p, q} {
		got, _ := s.Get(ctx, x.Slug)
		if got.Status != pods.StatusPaused {
			t.Fatalf("%s not paused: %s", x.Slug, got.Status)
		}
	}
	if len(res.Messages) != 1 || res.Clients[portalClient] == "" || !f.pulled["m1"] {
		t.Fatalf("message not passed on: %+v", res)
	}
	hold, err := s.PortalHold(ctx)
	if err != nil || len(hold) != 0 {
		t.Fatalf("healthy pods held: %v %v", hold, err)
	}
	// Approvals are per pod: each pod's ICP is its own row.
	if len(f.approvals) != 2 {
		t.Fatalf("approvals %d", len(f.approvals))
	}
}

func TestRelinkChecksTheClientAndClearsTheOldOne(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	f := newFake()
	if _, err := Relink(ctx, s, f, p.Slug, "00000000-0000-4000-8000-00000000dead"); err != ErrNoSuchClient {
		t.Fatalf("unknown client accepted: %v", err)
	}
	name, err := Relink(ctx, s, f, p.Slug, otherClient)
	if err != nil || name == "" {
		t.Fatal(name, err)
	}
	if len(f.cleared) != 1 || f.cleared[0] != portalClient+"/"+p.Slug {
		t.Fatalf("old client not cleared: %v", f.cleared)
	}
	got, _ := s.Get(ctx, p.Slug)
	if got.PortalClientID == nil || *got.PortalClientID != otherClient {
		t.Fatal("not relinked")
	}
}
