package pods

// Hand-off bundle (M8). "You own everything": when TFG hands the AI SDR over,
// the client gets one zip with their playbook and documents, the AI SDR's
// instructions, every email sent, their pipeline, meetings with briefs and
// coaching, weekly results and reviews, and the settings to run it themselves.
// It holds only this pod's data and never a key or password.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// BundleFiles builds the hand-off files for the pod: path → content.
func (s *Store) BundleFiles(ctx context.Context, slug string) (map[string][]byte, error) {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	docs, err := s.docsFor(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, key := range DocKeys {
		d, err := s.Doc(ctx, slug, key)
		if err != nil {
			continue
		}
		approved := "not approved yet"
		if d.ApprovedAt != nil {
			approved = "approved " + d.ApprovedAt.Format("2 Jan 2006")
		}
		files["documents/"+key+".md"] = []byte(fmt.Sprintf("# %s\n\nVersion %d, %s.\n\n%s\n", key, d.Version, approved, d.Body))
	}
	files["ai-sdr/instructions.md"] = []byte(Instructions(p, docs))
	settings, _ := json.MarshalIndent(map[string]any{
		"client": p.ClientName, "status": p.Status, "sender": map[string]string{"name": p.SenderName, "email": p.SenderEmail, "domain": p.SendingDomain},
		"approvalPolicy": p.ApprovalPolicy, "firstEmailDailyCap": p.FirstEmailDailyCap, "followUpDailyCap": p.FollowUpDailyCap,
		"timezone": p.Timezone, "sendWindow": []int{p.SendWindowStart, p.SendWindowEnd}, "sendWeekends": p.SendWeekends,
		"crm": p.CRMProvider, "calendar": p.CalendarProvider, "bookingUrl": p.BookingURL, "handoffEmail": p.HandoffEmail,
		"wentLiveAt": p.WentLiveAt,
	}, "", "  ")
	files["settings.json"] = settings

	if err := s.csvFile(ctx, files, "emails-sent.csv", []string{"sent_at", "to", "name", "company", "subject", "body", "sequence"},
		`SELECT coalesce(to_char(sent_at, 'YYYY-MM-DD HH24:MI'), ''), to_address, coalesce(to_name, ''), coalesce(company, ''), subject, body, coalesce(sequence_key, '')
		 FROM founderos_outbox WHERE workspace_id = $1 AND status = 'sent' ORDER BY sent_at`, p.WorkspaceID); err != nil {
		return nil, err
	}
	if err := s.csvFile(ctx, files, "pipeline.csv", []string{"name", "email", "company", "role", "status", "next_action", "research", "website", "linkedin"},
		`SELECT coalesce(name, ''), coalesce(email, ''), coalesce(company, ''), coalesce(role, ''), coalesce(status, ''), coalesce(next_action, ''),
		 coalesce(research_evidence, ''), coalesce(website, ''), coalesce(linkedin, '')
		 FROM founderos_funnel_contacts WHERE workspace_id = $1 ORDER BY created_at`, p.WorkspaceID); err != nil {
		return nil, err
	}
	if err := s.csvFile(ctx, files, "opt-outs.csv", []string{"address", "domain", "reason", "at"},
		// The client's own opt-outs in full; an installation-wide opt-out only when it covers one of
		// this client's own contacts, and then without the reason, which belongs to another sender.
		`SELECT address, domain, reason, at FROM (
		   SELECT coalesce(address, '') AS address, coalesce(domain, '') AS domain, coalesce(reason, '') AS reason, to_char(created_at, 'YYYY-MM-DD') AS at, created_at
		     FROM founderos_email_suppression WHERE workspace_id = $1
		   UNION ALL
		   SELECT coalesce(g.address, ''), coalesce(g.domain, ''), 'opted out of all outreach', to_char(g.created_at, 'YYYY-MM-DD'), g.created_at
		     FROM founderos_email_suppression g
		    WHERE g.workspace_id IS NULL AND EXISTS (
		      SELECT 1 FROM founderos_funnel_contacts c WHERE c.workspace_id = $1 AND c.email IS NOT NULL
		         AND (lower(c.email) = lower(g.address) OR split_part(lower(c.email), '@', 2) = lower(g.domain)))
		 ) s ORDER BY created_at`, p.WorkspaceID); err != nil {
		return nil, err
	}
	if err := s.csvFile(ctx, files, "meetings.csv", []string{"starts_at", "title", "attendee", "email", "status", "outcome", "brief", "coaching"},
		`SELECT coalesce(to_char(starts_at, 'YYYY-MM-DD HH24:MI'), ''), title, attendee_name, attendee_email, status, outcome, brief, coaching
		 FROM founderos_pod_meetings WHERE pod_id = $1 ORDER BY starts_at`, p.ID); err != nil {
		return nil, err
	}
	if err := s.csvFile(ctx, files, "approvals.csv", []string{"created_at", "kind", "title", "status", "approved_wording"},
		`SELECT to_char(created_at, 'YYYY-MM-DD'), kind, title, status, coalesce(payload::text, '')
		 FROM founderos_client_approvals WHERE pod_id = $1 ORDER BY created_at`, p.ID); err != nil {
		return nil, err
	}
	if err := s.csvFile(ctx, files, "activity.csv", []string{"at", "kind", "summary"},
		`SELECT to_char(at, 'YYYY-MM-DD HH24:MI'), kind, summary FROM founderos_client_activity WHERE pod_id = $1 ORDER BY at, id`, p.ID); err != nil {
		return nil, err
	}
	weeks, err := s.Weeks(ctx, p.ID, 520)
	if err != nil {
		return nil, err
	}
	var wb bytes.Buffer
	w := csv.NewWriter(&wb)
	_ = w.Write([]string{"week", "dry_run", "prospects_reached", "emails_sent", "replies", "reply_rate_pct", "enquiries", "handoffs", "meetings_booked", "meetings_held", "no_shows", "opt_outs"})
	sort.Slice(weeks, func(i, j int) bool { return weeks[i].Start < weeks[j].Start })
	for _, wk := range weeks {
		m := wk.M
		_ = w.Write([]string{wk.Start, fmt.Sprint(m.DryRun), fmt.Sprint(m.ProspectsReached), fmt.Sprint(m.EmailsSent), fmt.Sprint(m.Replies),
			fmt.Sprint(m.ReplyRatePct), fmt.Sprint(m.Enquiries), fmt.Sprint(m.Handoffs), fmt.Sprint(m.MeetingsBooked),
			fmt.Sprint(m.MeetingsHeld), fmt.Sprint(m.NoShows), fmt.Sprint(m.OptOuts)})
		if wk.Review != "" {
			files["weekly-reviews/"+wk.Start+".md"] = []byte("# Week of " + wk.Start + "\n\n" + metricLines(m) + "\n" + wk.Review + "\n")
		}
	}
	w.Flush()
	files["weekly-results.csv"] = wb.Bytes()
	base, _ := s.GetBaseline(ctx, p.ID)
	files["README.md"] = []byte(bundleReadme(p, base, weeks, files))
	return files, nil
}

func (s *Store) csvFile(ctx context.Context, files map[string][]byte, name string, header []string, q string, args ...any) error {
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	defer rows.Close()
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(header)
	for rows.Next() {
		vals := make([]string, len(header))
		ptrs := make([]any, len(header))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		_ = w.Write(vals)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	w.Flush()
	files[name] = b.Bytes()
	return nil
}

func bundleReadme(p Pod, b Baseline, weeks []Week, files map[string][]byte) string {
	var sb strings.Builder
	sb.WriteString("# " + p.ClientName + ": your AI SDR, handed over\n\n")
	sb.WriteString("Prepared by The Founders Group on " + time.Now().Format("2 January 2006") + ". Everything here is yours.\n\n")
	booked, held, reached, replies := 0, 0, 0, 0
	for _, w := range weeks {
		booked += w.M.MeetingsBooked
		held += w.M.MeetingsHeld
		reached += w.M.ProspectsReached
		replies += w.M.Replies
	}
	sb.WriteString(fmt.Sprintf("## Results\n\n%d weeks recorded: %d prospects reached, %d replies, %d meetings booked, %d held.\n", len(weeks), reached, replies, booked, held))
	if b.SetAt != "" {
		sb.WriteString(fmt.Sprintf("Before TFG: %d meetings a month, %.1f%% reply rate (%s).\n", b.MeetingsPerMonth, b.ReplyRatePct, b.Note))
	}
	sb.WriteString("\n## What is inside\n\n")
	var names []string
	for k := range files {
		names = append(names, k)
	}
	names = append(names, "README.md")
	sort.Strings(names)
	desc := map[string]string{
		"settings.json":          "how the AI SDR sends: mailbox, daily limits, hours, approvals, CRM and calendar",
		"ai-sdr/instructions.md": "the AI SDR's full instructions, built from your approved documents; give them to any AI agent to run it yourself",
		"emails-sent.csv":        "every email sent to a prospect, with its wording",
		"pipeline.csv":           "every prospect researched, their status, next step and the research behind them",
		"opt-outs.csv":           "people who asked not to be contacted; keep honouring these",
		"meetings.csv":           "every meeting booked, with its call brief and coaching notes",
		"approvals.csv":          "what you approved along the way",
		"activity.csv":           "everything the AI SDR did, in order",
		"weekly-results.csv":     "the numbers, week by week",
		"README.md":              "this guide",
	}
	for _, n := range names {
		d := desc[n]
		switch {
		case strings.HasPrefix(n, "documents/"):
			d = "your playbook document"
		case strings.HasPrefix(n, "weekly-reviews/"):
			d = "that week's review and what to work on"
		}
		sb.WriteString("- `" + n + "`: " + d + "\n")
	}
	sb.WriteString("\n## Running it yourself\n\n" +
		"1. Keep sending from your own mailbox (" + p.SenderEmail + ") within the limits in `settings.json`; they protect your domain's reputation.\n" +
		"2. Give `ai-sdr/instructions.md` to your AI agent of choice, with `pipeline.csv` as its starting list.\n" +
		"3. Never email anyone in `opt-outs.csv`.\n" +
		"4. Keep the weekly review: count reached, replies and meetings every Monday and pick three things to work on.\n\n" +
		"Questions: nader@thefoundersgroup.net\n")
	return sb.String()
}

// Bundle zips the hand-off files.
func (s *Store) Bundle(ctx context.Context, slug string) ([]byte, error) {
	files, err := s.BundleFiles(ctx, slug)
	if err != nil {
		return nil, err
	}
	var names []string
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, n := range names {
		f, err := z.Create(n)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
