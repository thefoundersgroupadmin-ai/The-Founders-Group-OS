package pods

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func TestAnInboundLinkAcceptsEnquiriesForItsPodOnly(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Brightvale")
	other := newPod(t, s, "Kestrel")
	token, err := s.NewInboundToken(ctx, p.Slug)
	if err != nil || len(token) < 32 {
		t.Fatalf("token %q %v", token, err)
	}
	if _, err := s.Intake(ctx, token, Enquiry{Email: "tom@harbour.example"}, "ip1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a pod in setup took an enquiry: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET status = 'live' WHERE slug = $1`, p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Intake(ctx, "wrong-token", Enquiry{Email: "tom@harbour.example"}, "ip1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad token accepted: %v", err)
	}
	if _, err := s.Intake(ctx, token, Enquiry{Email: "not an email"}, "ip1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad email accepted: %v", err)
	}
	id, err := s.Intake(ctx, token, Enquiry{Name: "Tom Reid", Email: "Tom@Harbour.example", Company: "Harbourline",
		Message: "Do you handle two warehouses?"}, "ip1")
	if err != nil || id == 0 {
		t.Fatalf("intake: %d %v", id, err)
	}
	var ws, status, venture string
	if err := s.Pool.QueryRow(ctx, `SELECT workspace_id::text, status, venture FROM founderos_funnel_contacts WHERE lower(email) = 'tom@harbour.example'`).Scan(&ws, &status, &venture); err != nil {
		t.Fatal(err)
	}
	if ws != p.WorkspaceID || status != "engaged" || venture != "client" || ws == other.WorkspaceID {
		t.Fatalf("contact %s %s %s", ws, status, venture)
	}
	// Spam traps: a filled honeypot is dropped silently; a flood is refused.
	if id, err := s.Intake(ctx, token, Enquiry{Email: "bot@x.example", Honeypot: "http://spam"}, "ip2"); err != nil || id != 0 {
		t.Fatalf("honeypot: %d %v", id, err)
	}
	for i := 0; i < 20; i++ {
		_, err = s.Intake(ctx, token, Enquiry{Email: "flood@x.example"}, "ip3")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("no rate limit: %v", err)
	}
	// A new token replaces the old one.
	t2, _ := s.NewInboundToken(ctx, p.Slug)
	if _, err := s.Intake(ctx, token, Enquiry{Email: "a@b.example"}, "ip4"); !errors.Is(err, ErrNotFound) || t2 == token {
		t.Fatalf("old token still works: %v", err)
	}
}

// fakeTasks records tasks the inbound tasker opens.
type fakeTasks struct{ titles, bodies []string }

func (f *fakeTasks) CreateTask(_ context.Context, projectID, agentID, title, description string) (string, error) {
	f.titles = append(f.titles, title)
	f.bodies = append(f.bodies, description)
	return "issue-" + string(rune('a'+len(f.titles))), nil
}

func TestEachEnquiryBecomesATaskForThePodsOwnAgentAndAConversation(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	b := newFakeBoard()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.Provision(ctx, b, p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET status = 'live' WHERE slug = $1`, p.Slug); err != nil {
		t.Fatal(err)
	}
	token, _ := s.NewInboundToken(ctx, p.Slug)
	if _, err := s.Intake(ctx, token, Enquiry{Name: "Tom", Email: "tom@harbour.example", Company: "Harbourline", Message: "Two warehouses?"}, "ip"); err != nil {
		t.Fatal(err)
	}
	f := &fakeTasks{}
	n, err := s.TaskInbound(ctx, f)
	if err != nil || n != 1 || len(f.titles) != 1 {
		t.Fatalf("tasked %d %v", n, err)
	}
	for _, want := range []string{"Two warehouses?", "budget", "timing", "decides", "Sam Reed", `"outbox"`, `"handoff"`} {
		if !strings.Contains(f.bodies[0], want) {
			t.Errorf("task lacks %q", want)
		}
	}
	if strings.Contains(f.bodies[0], "Nader") || strings.Contains(f.bodies[0], "Founders Group") {
		t.Error("task leaks TFG context")
	}
	var conv int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM founderos_pod_conversations`).Scan(&conv)
	if conv != 1 {
		t.Fatalf("no conversation recorded: %d", conv)
	}
	if n, _ := s.TaskInbound(ctx, f); n != 0 || len(f.titles) != 1 {
		t.Fatal("an enquiry was tasked twice")
	}
}

// Codex fix review #3: a buyer a person took over who writes in again through
// the website form goes to the person, not the AI.
func TestARepeatEnquiryAfterATakeoverGoesToThePerson(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	b := newFakeBoard()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.Provision(ctx, b, p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	token, err := s.NewInboundToken(ctx, p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Intake(ctx, token, Enquiry{Name: "Cara", Email: "cara@globex.example", Message: "Pricing?"}, "web"); err != nil {
		t.Fatal(err)
	}
	f := &fakeTasks{}
	if k, err := s.TaskInbound(ctx, f); err != nil || k != 1 {
		t.Fatalf("first enquiry: %d %v", k, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_conversations SET taken_over_at = now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Intake(ctx, token, Enquiry{Name: "Cara", Email: "cara@globex.example", Message: "Following up on pricing"}, "web"); err != nil {
		t.Fatal(err)
	}
	if k, err := s.TaskInbound(ctx, f); err != nil || k != 0 || len(f.bodies) != 1 {
		t.Fatalf("repeat enquiry went to the AI: %d %v", k, err)
	}
	var n int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM founderos_pod_handoffs WHERE issue_id LIKE 'form:%' AND summary LIKE '%Following up%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("hand-off to the person: %d", n)
	}
}
