package pods

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func TestTheHandoffBundleHoldsEverythingTheClientOwnsAndNothingElse(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Brightvale")
	other := readyPod(t, s, "Kestrel")
	_ = s.SetDoc(ctx, other.Slug, "icp", "KESTREL-SECRET-ICP")
	if err := s.ApproveDoc(ctx, p.Slug, "icp", "Priya by email"); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body, status, sent_at)
		VALUES ('o1', $1, 'test', 'ops@acme.example', 'Stock counts', 'Hi, quick one about counts', 'sent', now())`, p.WorkspaceID)
	exec(`INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body, status, sent_at)
		VALUES ('o2', $1, 'test', 'x@kestrel-lead.example', 'KESTREL-EMAIL', 'b', 'sent', now())`, other.WorkspaceID)
	exec(`INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email, company)
		VALUES ('c1', $1, 'Ana Ops', 'engaged', 'client', now(), 'ops@acme.example', 'Acme')`, p.WorkspaceID)
	exec(`INSERT INTO founderos_pod_meetings (pod_id, booking_uid, attendee_email, title, brief, coaching)
		VALUES ($1, 'b1', 'ops@acme.example', 'Discovery', 'Ana runs ops', 'Ask about budget')`, p.ID)
	exec(`INSERT INTO founderos_pod_weekly (pod_id, week_start, metrics, review)
		VALUES ($1, '2026-11-02', '{"week":"2026-11-02","meetingsBooked":2,"prospectsReached":40}', 'Good week')`, p.ID)
	exec(`INSERT INTO founderos_email_suppression (workspace_id, address, reason) VALUES ($1, 'no@acme.example', 'asked')`, p.WorkspaceID)
	// Installation-wide opt-outs: only one relates to this client's own contacts.
	exec(`INSERT INTO founderos_email_suppression (workspace_id, address, reason) VALUES (NULL, 'ops@acme.example', 'GLOBAL-REASON-1')`)
	exec(`INSERT INTO founderos_email_suppression (workspace_id, address, reason) VALUES (NULL, 'someone@unrelated-global.example', 'GLOBAL-REASON-2')`)
	if err := s.SetBaseline(ctx, p.Slug, Baseline{MeetingsPerMonth: 3, ReplyRatePct: 1, Note: "founder outbound"}); err != nil {
		t.Fatal(err)
	}

	raw, err := s.Bundle(ctx, p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		all[f.Name] = b.String()
	}
	for _, name := range []string{"README.md", "settings.json", "ai-sdr/instructions.md", "documents/icp.md", "emails-sent.csv", "pipeline.csv",
		"opt-outs.csv", "meetings.csv", "approvals.csv", "activity.csv", "weekly-results.csv", "weekly-reviews/2026-11-02.md"} {
		if _, ok := all[name]; !ok {
			t.Errorf("bundle lacks %s", name)
		}
	}
	if !strings.Contains(all["documents/icp.md"], "approved") || !strings.Contains(all["emails-sent.csv"], "quick one about counts") ||
		!strings.Contains(all["pipeline.csv"], "Ana Ops") || !strings.Contains(all["meetings.csv"], "Ask about budget") ||
		!strings.Contains(all["opt-outs.csv"], "no@acme.example") || !strings.Contains(all["opt-outs.csv"], "ops@acme.example") || !strings.Contains(all["weekly-reviews/2026-11-02.md"], "Good week") ||
		!strings.Contains(all["README.md"], "3 meetings a month") || !strings.Contains(all["README.md"], "2 meetings booked") {
		t.Fatal("bundle content incomplete")
	}
	joined := strings.Join(func() []string {
		var v []string
		for _, s := range all {
			v = append(v, s)
		}
		return v
	}(), "\n")
	for _, leak := range []string{"KESTREL", "kestrel-lead", "PASS", "password", "token", "unrelated-global", "GLOBAL-REASON"} {
		if strings.Contains(joined, leak) {
			t.Errorf("bundle leaks %q", leak)
		}
	}
}
