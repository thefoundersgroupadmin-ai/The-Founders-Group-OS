package api

import (
	"context"
	"encoding/json"
	"github.com/rhl/businessos-backend/internal/founderos/pages/signals"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
)

// Installation mail/calendar/Slack belong to the owner's HQ. They must not
// silently become a source for every client workspace that enables a profile.
func connectedSalesSnapshot(ctx context.Context, d *Deps, c saleskit.AutomationConfig) (saleskit.Snapshot, error) {
	snap, err := (saleskit.AutomationStore{Pool: d.Pool}).SnapshotFor(ctx, c)
	if err != nil || c.Workspace != "founderos" {
		return snap, err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	snap.Gaps = []string{"Contract documents are not connected to this automation. Calendar results describe observed events, not a guarantee of free availability."}
	// Match email to authorised prospect addresses/domains, excluding shared
	// mailbox domains. This avoids feeding unrelated personal inbox content.
	ids := append([]string{c.WorkspaceID}, c.SourceWorkspaceIDs...)
	rows, err := d.Pool.Query(ctx, `SELECT email FROM founderos_funnel_contacts WHERE workspace_id=ANY($1::uuid[]) AND email IS NOT NULL`, ids)
	if err != nil {
		return snap, err
	}
	addresses, domains := map[string]bool{}, map[string]bool{}
	free := map[string]bool{"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "yahoo.com": true, "icloud.com": true, "proton.me": true, "protonmail.com": true}
	for rows.Next() {
		var address string
		if rows.Scan(&address) == nil {
			address = strings.ToLower(strings.TrimSpace(address))
			addresses[address] = true
			if parts := strings.Split(address, "@"); len(parts) == 2 && !free[parts[1]] {
				domains[parts[1]] = true
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return snap, err
	}
	src := commsSourcesFor(d)
	var mu sync.Mutex
	var wg sync.WaitGroup
	add := func(id, title string, value any, max int) {
		raw, _ := json.Marshal(value)
		if len(raw) == 0 || string(raw) == "[]" || string(raw) == "null" {
			return
		}
		text := clip(string(raw), max)
		mu.Lock()
		snap.Sources = append(snap.Sources, saleskit.Source{ID: id, Title: title, Text: text})
		mu.Unlock()
	}
	gap := func(s string) { mu.Lock(); snap.Gaps = append(snap.Gaps, s); mu.Unlock() }
	wg.Add(4)
	go func() {
		defer wg.Done()
		rows, _, e := signals.Board(ctx, d.Pool, c.WorkspaceID, 40)
		if e != nil {
			gap("Buying signals unavailable")
			return
		}
		type brief struct {
			ContactID string   `json:"contactId"`
			Company   string   `json:"company"`
			Person    string   `json:"person,omitempty"`
			Score     int      `json:"score"`
			WhyNow    string   `json:"whyNow"`
			Offer     string   `json:"offer,omitempty"`
			Evidence  []string `json:"evidence"`
		}
		var out []brief
		for _, r := range rows {
			if r.Score <= 0 {
				continue
			}
			b := brief{ContactID: r.ContactID, Company: r.Company, Person: r.Person, Score: r.Score, WhyNow: r.WhyNow, Offer: r.Plays.Offer}
			for _, s := range r.Signals {
				if s.Weight > 0 {
					b.Evidence = append(b.Evidence, strings.TrimSpace(s.Title+" "+s.URL))
				}
			}
			out = append(out, b)
		}
		add("os-buying-signals", "Buying signals from public sources (job boards, website tools, recorded funding); cite the evidence, never go beyond it", out, 12000)
	}()
	go func() {
		defer wg.Done()
		if src.Emails == nil {
			return
		}
		items, e := src.Emails(ctx)
		if e != nil {
			gap("Prospect email read unavailable; review Connections → Email")
			return
		}
		filtered := append(items[:0:0], items...)[:0]
		for _, it := range items {
			sender := strings.ToLower(it.Sender)
			if a, e := mail.ParseAddress(it.Sender); e == nil {
				sender = strings.ToLower(a.Address)
			}
			parts := strings.Split(sender, "@")
			if addresses[sender] || (len(parts) == 2 && domains[parts[1]]) {
				filtered = append(filtered, it)
			}
		}
		add("os-prospect-email", "Observed prospect email previews (not complete threads)", filtered, 10000)
	}()
	go func() {
		defer wg.Done()
		if src.Events == nil {
			return
		}
		items, e := src.Events(ctx)
		if e != nil {
			gap("Calendar read unavailable; review Connections → Calendar")
			return
		}
		add("os-calendar", "Observed owner calendar events", items, 8000)
	}()
	go func() {
		defer wg.Done()
		m := RosterDeps(d).Memory
		if m == nil {
			gap("Knowledge engine is unavailable")
			return
		}
		hits, e := m.Search(ctx, "sales offer qualification outreach process", []string{c.Workspace}, 5)
		if e != nil {
			gap("Knowledge search incomplete; review Brain connection")
		}
		add("os-sales-knowledge", "Workspace knowledge search snippets; verify approval and freshness", hits, 8000)
	}()
	wg.Wait()
	// Stable source order and a strict total budget keep repeated polls and
	// digest comparisons deterministic, regardless of read completion order.
	sort.Slice(snap.Sources, func(i, j int) bool { return snap.Sources[i].ID < snap.Sources[j].ID })
	extra := []saleskit.Source{}
	remaining := 110000
	for i := range snap.Sources {
		if len(snap.Sources[i].Text) > remaining {
			snap.Sources[i].Text = strings.ToValidUTF8(snap.Sources[i].Text[:max(remaining, 0)], "") + "\n[excerpt]"
			snap.Gaps = append(snap.Gaps, "Source packet truncated to the execution budget")
		}
		remaining -= len(snap.Sources[i].Text)
		if snap.Sources[i].ID == "os-prospect-email" || snap.Sources[i].ID == "os-calendar" {
			extra = append(extra, snap.Sources[i])
		}
	}
	if len(extra) > 0 {
		snap.ContextRevision = saleskit.Digest(extra)
	}
	return snap, nil
}
