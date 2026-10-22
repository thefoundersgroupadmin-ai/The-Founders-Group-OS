package outbox

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A pre-approved campaign (the owner, 2026-10-22: "send outreach with my
// preapproval, I don't want to approve anything else") sends its first-contact
// emails without a click. Only rows the agents filed on a campaign task
// (source paperclip, source_ref = that task's identifier) qualify, and each
// one still has to pass Screen. Limits: Campaign.DailyCap in any 24 hours,
// Campaign.TotalCap ever, inside Campaign.Window.

// Campaign is one pre-approved outreach run.
type Campaign struct {
	Ref      string // the Paperclip task identifier, e.g. THE-21
	DailyCap int
	TotalCap int
	// Window says whether now is a sending time (weekday US business hours).
	Window func(time.Time) bool
}

var (
	placeholder = regexp.MustCompile(`(?i)\{\{|\}\}|\[(?:first ?name|name|company|your name|insert[^\]]*|todo|tbd)\]|<[A-Z_ ]{3,}>|(?i)\bTODO\b|(?i)lorem ipsum`)
	optOut      = regexp.MustCompile(`(?i)\b(no thanks|not relevant|unsubscribe|opt out|let me know if you'?d rather not)\b`)
)

// Screen is what a first email must pass to go out unseen: a real address
// that isn't ours, a subject, a body of a sane length that names the sender,
// gives an easy way out, and carries no template leftovers.
func Screen(it Item, ownDomain string) error {
	addr, err := mail.ParseAddress(it.To)
	if err != nil || !strings.Contains(addr.Address, ".") || addr.Address != strings.TrimSpace(it.To) {
		return fmt.Errorf("address %q is not a plain email address", it.To)
	}
	if ownDomain != "" && strings.HasSuffix(strings.ToLower(addr.Address), "@"+strings.ToLower(ownDomain)) {
		return errors.New("addressed to our own domain")
	}
	subj, body := strings.TrimSpace(it.Subject), strings.TrimSpace(it.Body)
	switch {
	case subj == "" || len(subj) > 120:
		return errors.New("subject missing or too long")
	case len(body) < 200 || len(body) > 2500:
		return fmt.Errorf("body is %d characters (want 200 to 2500)", len(body))
	case placeholder.MatchString(subj + "\n" + body):
		return errors.New("template placeholder left in the email")
	case !optOut.MatchString(body):
		return errors.New("no opt-out line")
	case !strings.Contains(body, "Nader"):
		return errors.New("not signed")
	}
	return nil
}

// campaignDue: waiting rows of the campaign to an address and a company never
// emailed, never rejected, whose pipeline contact (if any) is still untouched.
const campaignDue = `
	SELECT ` + cols + ` FROM founderos_outbox o
	WHERE o.workspace_id = $1 AND o.status = 'pending' AND o.source = 'paperclip' AND o.source_ref = $2
	  AND NOT EXISTS (SELECT 1 FROM founderos_outbox p WHERE p.workspace_id = o.workspace_id AND p.id <> o.id
	       AND p.status IN ('sent', 'sending', 'uncertain', 'rejected')
	       AND (lower(p.to_address) = lower(o.to_address)
	            OR split_part(lower(p.to_address), '@', 2) = split_part(lower(o.to_address), '@', 2)))
	  AND NOT EXISTS (SELECT 1 FROM founderos_funnel_contacts c WHERE c.id = o.contact_id AND c.status <> 'first_touch')
	ORDER BY o.created_at, o.id`

const campaignSent = `SELECT
	count(*) FILTER (WHERE sent_at > now() - interval '24 hours'),
	count(*)
	FROM founderos_outbox WHERE workspace_id = $1 AND source = 'paperclip' AND source_ref = $2 AND status = 'sent'`

// CampaignResult is one pass.
type CampaignResult struct {
	Sent     int
	Held     map[string]string // row id → why it was not sent (left pending for a look)
	Failures int               // sends that failed this pass
}

// AutoSendCampaign sends the campaign's due first emails one per call: a pass
// every few minutes spreads them out instead of a burst.
func AutoSendCampaign(ctx context.Context, pool *pgxpool.Pool, workspaceSlug string, c Campaign, ownDomain string, now time.Time, send SendFunc) (CampaignResult, error) {
	res := CampaignResult{Held: map[string]string{}}
	if c.Ref == "" || (c.Window != nil && !c.Window(now)) {
		return res, nil
	}
	ws, err := workspaceID(ctx, pool, workspaceSlug)
	if err != nil {
		return res, err
	}
	var today, total int
	if err := pool.QueryRow(ctx, campaignSent, ws, c.Ref).Scan(&today, &total); err != nil {
		return res, err
	}
	if today >= c.DailyCap || total >= c.TotalCap {
		return res, nil
	}
	rows, err := pool.Query(ctx, campaignDue, ws, c.Ref)
	if err != nil {
		return res, err
	}
	var due []Item
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			rows.Close()
			return res, err
		}
		due = append(due, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	seenDomain := map[string]bool{}
	for _, it := range due {
		domain := strings.ToLower(it.To[strings.LastIndex(it.To, "@")+1:])
		if seenDomain[domain] {
			res.Held[it.ID] = "a second person at the same company in this campaign"
			continue
		}
		seenDomain[domain] = true
		if err := Screen(it, ownDomain); err != nil {
			res.Held[it.ID] = err.Error()
			continue
		}
		got, err := Approve(ctx, pool, workspaceSlug, it.ID, Content{Subject: it.Subject, Body: it.Body}, send)
		if got != nil && got.Status == "sent" {
			res.Sent++
		}
		if err != nil && !errors.Is(err, ErrNotPending) {
			res.Failures++
			return res, err
		}
		return res, nil // one per pass
	}
	return res, nil
}

// USBusinessHours is Monday to Friday, 9am to 5pm US Eastern.
func USBusinessHours(now time.Time) bool {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return false
	}
	t := now.In(loc)
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday && t.Hour() >= 9 && t.Hour() < 17
}
