package crmsync

// Inbound: what the client's CRM knows that must stop outreach (gap list, 11 Nov 2026). People who
// opted out of email in the client's CRM, and companies that are already the client's customers,
// are added to the pod's do-not-contact list, so the AI SDR never emails them. Reads only; the
// CRM is never changed by this.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Blocked is one address or whole domain the client's CRM says must not be contacted.
type Blocked struct {
	Address, Domain, Reason string
}

// Blocklister is a CRM that can say who must not be contacted.
type Blocklister interface {
	Blocked(ctx context.Context) ([]Blocked, error)
}

const (
	reasonOptOut   = "opted out of email in the client's CRM"
	reasonCustomer = "already a customer in the client's CRM"
	maxInbound     = 20000
)

// ErrIncomplete: the CRM has more opt-outs or customers than one pass reads; outreach holds
// rather than carry on with a partial list.
var ErrIncomplete = errors.New("the CRM's opt-out or customer list is larger than one read allows; outreach is held until it can be read in full")

// domainOf turns a website or address into a bare domain ("https://www.acme.com/x" → "acme.com").
func domainOf(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	if !strings.Contains(s, ".") || strings.ContainsAny(s, " @") {
		return ""
	}
	return s
}

// Reconcile adds the CRM's opt-outs and customers to the pod's do-not-contact list and withdraws
// anything still waiting to go to them. It returns how many new entries were added.
func Reconcile(ctx context.Context, pool *pgxpool.Pool, podID string, crm CRM) (int, error) {
	bl, ok := crm.(Blocklister)
	if !ok {
		return 0, nil
	}
	list, err := bl.Blocked(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading %s opt-outs and customers: %w", crm.Name(), err)
	}
	var ws string
	if err := pool.QueryRow(ctx, `SELECT workspace_id::text FROM founderos_client_pods WHERE id = $1`, podID).Scan(&ws); err != nil {
		return 0, err
	}
	// Under the pod's send-gate lock, so a send in flight finishes first and none follows unchecked.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('founderos-pod-send:' || $1))`, ws); err != nil {
		return 0, err
	}
	added := 0
	for _, b := range list {
		addr, dom := strings.ToLower(strings.TrimSpace(b.Address)), domainOf(b.Domain)
		if freeMail[dom] {
			dom = ""
		}
		if addr == "" && dom == "" {
			continue
		}
		var tag int64
		if addr != "" {
			t, err := tx.Exec(ctx, `INSERT INTO founderos_email_suppression (workspace_id, address, reason)
				SELECT $1, $2, $3 WHERE NOT EXISTS (SELECT 1 FROM founderos_email_suppression WHERE workspace_id = $1 AND address = $2)`, ws, addr, b.Reason)
			if err != nil {
				return added, err
			}
			tag = t.RowsAffected()
			if _, err := tx.Exec(ctx, `UPDATE founderos_outbox SET status = 'rejected', decided_at = now(), error = $3
				WHERE workspace_id = $1 AND lower(to_address) = $2 AND status = 'pending'`, ws, addr, b.Reason); err != nil {
				return added, err
			}
		} else {
			t, err := tx.Exec(ctx, `INSERT INTO founderos_email_suppression (workspace_id, domain, reason)
				SELECT $1, $2, $3 WHERE NOT EXISTS (SELECT 1 FROM founderos_email_suppression WHERE workspace_id = $1 AND domain = $2)`, ws, dom, b.Reason)
			if err != nil {
				return added, err
			}
			tag = t.RowsAffected()
			if _, err := tx.Exec(ctx, `UPDATE founderos_outbox SET status = 'rejected', decided_at = now(), error = $3
				WHERE workspace_id = $1 AND split_part(lower(to_address), '@', 2) = $2 AND status = 'pending'`, ws, dom, b.Reason); err != nil {
				return added, err
			}
		}
		added += int(tag)
	}
	return added, tx.Commit(ctx)
}

// HubSpot: contacts with hs_email_optout, companies whose lifecycle stage is customer.
func (h HubSpot) Blocked(ctx context.Context) ([]Blocked, error) {
	var out []Blocked
	search := func(object, prop, value, field, reason string, asDomain bool) error {
		after := ""
		for read := 0; ; read += 100 {
			if read >= maxInbound {
				return ErrIncomplete
			}
			body := map[string]any{"filterGroups": []any{map[string]any{"filters": []any{map[string]any{
				"propertyName": prop, "operator": "EQ", "value": value}}}}, "properties": []string{field}, "limit": 100}
			if after != "" {
				body["after"] = after
			}
			var page struct {
				Results []struct {
					Properties map[string]string `json:"properties"`
				} `json:"results"`
				Paging struct {
					Next struct {
						After string `json:"after"`
					} `json:"next"`
				} `json:"paging"`
			}
			if err := callRead(ctx, h.base()+"/crm/v3/objects/"+object+"/search", h.hdr(), body, &page); err != nil {
				return err
			}
			for _, r := range page.Results {
				v := r.Properties[field]
				if v == "" {
					continue
				}
				if asDomain {
					out = append(out, Blocked{Domain: v, Reason: reason})
				} else {
					out = append(out, Blocked{Address: v, Reason: reason})
				}
			}
			if after = page.Paging.Next.After; after == "" {
				return nil
			}
		}
	}
	if err := search("contacts", "hs_email_optout", "true", "email", reasonOptOut, false); err != nil {
		return nil, err
	}
	if err := search("companies", "lifecyclestage", "customer", "domain", reasonCustomer, true); err != nil {
		return nil, err
	}
	return out, nil
}

// Pipedrive: people whose marketing status is unsubscribed; the people on won deals (their domain).
func (p Pipedrive) Blocked(ctx context.Context) ([]Blocked, error) {
	var out []Blocked
	type email struct {
		Value string `json:"value"`
	}
	for start := 0; ; start += 500 {
		if start >= maxInbound {
			return nil, ErrIncomplete
		}
		var page struct {
			Data []struct {
				Email           []email `json:"email"`
				MarketingStatus string  `json:"marketing_status"`
			} `json:"data"`
			AdditionalData struct {
				Pagination struct {
					More bool `json:"more_items_in_collection"`
				} `json:"pagination"`
			} `json:"additional_data"`
		}
		if err := call(ctx, http.MethodGet, p.url("/persons", url.Values{"start": {fmt.Sprint(start)}, "limit": {"500"}}), nil, nil, &page); err != nil {
			return nil, err
		}
		for _, x := range page.Data {
			if strings.EqualFold(x.MarketingStatus, "unsubscribed") {
				for _, e := range x.Email {
					if e.Value != "" {
						out = append(out, Blocked{Address: e.Value, Reason: reasonOptOut})
					}
				}
			}
		}
		if !page.AdditionalData.Pagination.More {
			break
		}
	}
	for start := 0; ; start += 500 {
		if start >= maxInbound {
			return nil, ErrIncomplete
		}
		var page struct {
			Data []struct {
				PersonID *struct {
					Email []email `json:"email"`
				} `json:"person_id"`
			} `json:"data"`
			AdditionalData struct {
				Pagination struct {
					More bool `json:"more_items_in_collection"`
				} `json:"pagination"`
			} `json:"additional_data"`
		}
		if err := call(ctx, http.MethodGet, p.url("/deals", url.Values{"status": {"won"}, "start": {fmt.Sprint(start)}, "limit": {"500"}}), nil, nil, &page); err != nil {
			return nil, err
		}
		for _, d := range page.Data {
			if d.PersonID == nil {
				continue
			}
			for _, e := range d.PersonID.Email {
				if dom := domainOf(e.Value); dom != "" && !freeMail[dom] {
					out = append(out, Blocked{Domain: dom, Reason: reasonCustomer})
				}
			}
		}
		if !page.AdditionalData.Pagination.More {
			break
		}
	}
	return out, nil
}

// Salesforce: contacts with HasOptedOutOfEmail, accounts whose type starts with Customer.
func (s *Salesforce) Blocked(ctx context.Context) ([]Blocked, error) {
	tok, inst, err := s.auth(ctx)
	if err != nil {
		return nil, err
	}
	hdr := map[string]string{"Authorization": "Bearer " + tok}
	query := func(q string, each func(map[string]any)) error {
		next := "/services/data/" + sfVersion + "/query?q=" + url.QueryEscape(q)
		for n := 0; next != ""; n++ {
			if n >= maxInbound/200 {
				return ErrIncomplete
			}
			var page struct {
				Records        []map[string]any `json:"records"`
				NextRecordsURL string           `json:"nextRecordsUrl"`
			}
			if err := call(ctx, http.MethodGet, inst+next, hdr, nil, &page); err != nil {
				return err
			}
			for _, r := range page.Records {
				each(r)
			}
			next = page.NextRecordsURL
		}
		return nil
	}
	var out []Blocked
	if err := query("SELECT Email FROM Contact WHERE HasOptedOutOfEmail = true AND Email != null", func(r map[string]any) {
		if e, _ := r["Email"].(string); e != "" {
			out = append(out, Blocked{Address: e, Reason: reasonOptOut})
		}
	}); err != nil {
		return nil, err
	}
	if err := query("SELECT Website FROM Account WHERE Type LIKE 'Customer%' AND Website != null", func(r map[string]any) {
		if w, _ := r["Website"].(string); w != "" {
			out = append(out, Blocked{Domain: w, Reason: reasonCustomer})
		}
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// freeMail domains are never blocked as a whole: a customer using gmail.com doesn't make every
// gmail.com address a customer.
var freeMail = map[string]bool{"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true,
	"yahoo.com": true, "icloud.com": true, "me.com": true, "aol.com": true, "proton.me": true, "protonmail.com": true, "live.com": true}
