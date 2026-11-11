// Package portalsync keeps each client pod and its client's portal in step
// (docs/tfg/CLIENT-PODS.md, M2). TFG OS is the source of truth for the AI
// SDR; the portal is where the client sees it and decides. Each pass, for
// every pod linked to a portal client:
//
//   - push: the pod's state (status, sender, sending rules, what is missing),
//     its approval requests (re-opened in the portal whenever the wording
//     changed), meetings, weekly results, campaigns, pipeline and activity;
//   - pull: the client's decisions, applied only to the wording they saw (a
//     decision on older wording goes back to pending), pause/resume requests
//     (pause applies at once; resume only if the pod is ready) and the
//     client's support messages.
//
// A client may have several pods (Core has 2, The Full 4): every portal row
// carries the pod's slug. Pause and resume requests are read for every client
// before anything else, each with its own short budget, so a slow pod can't
// delay another client's pause; a request without a pod applies to all of
// that client's pods. Each pod's sync result is kept as its "portal" health,
// and sending holds when the portal's controls haven't been read recently
// (pods.PortalHold).
package portalsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

type Pod struct {
	ClientID       string   `json:"client_id"`
	PodSlug        string   `json:"pod_slug"`
	Name           string   `json:"name"`
	SyncedAt       string   `json:"synced_at"`
	Status         string   `json:"status"`
	ApprovalPolicy string   `json:"approval_policy"`
	Sender         string   `json:"sender"`
	SendingRules   string   `json:"sending_rules"`
	Missing        []string `json:"missing"`
	Baseline       any      `json:"baseline,omitempty"`
}

type Approval struct {
	ID            string          `json:"id,omitempty"`
	ClientID      string          `json:"client_id"`
	PodSlug       string          `json:"pod_slug"`
	OSKey         string          `json:"os_key"`
	Kind          string          `json:"kind"`
	Title         string          `json:"title"`
	Payload       json.RawMessage `json:"payload"`
	ContentHash   string          `json:"content_hash"`
	Status        string          `json:"status"`
	ClientNote    string          `json:"client_note,omitempty"`
	DecidedByName string          `json:"decided_by_name,omitempty"`
	DecidedAt     string          `json:"decided_at,omitempty"`
	Pulled        bool            `json:"-"`
}

type Activity struct {
	ClientID string `json:"client_id"`
	PodSlug  string `json:"pod_slug"`
	OSID     int64  `json:"os_id"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Summary  string `json:"summary"`
}

type Meeting struct {
	ClientID      string  `json:"client_id"`
	PodSlug       string  `json:"pod_slug"`
	OSID          int64   `json:"os_id"`
	Title         string  `json:"title"`
	Attendee      string  `json:"attendee"`
	StartsAt      *string `json:"starts_at"`
	Brief         string  `json:"brief"`
	Status        string  `json:"status"`
	Outcome       string  `json:"outcome"`
	Coaching      string  `json:"coaching"`
	RescheduleURL string  `json:"reschedule_url"`
	CancelURL     string  `json:"cancel_url"`
}

// Weekly is one week of results and its review, as the portal shows it.
type Weekly struct {
	ClientID  string       `json:"client_id"`
	PodSlug   string       `json:"pod_slug"`
	WeekStart string       `json:"week_start"`
	Metrics   pods.Metrics `json:"metrics"`
	Review    string       `json:"review"`
}

type Request struct {
	ID              string `json:"id"`
	ClientID        string `json:"client_id"`
	PodSlug         string `json:"pod_slug"`
	Kind            string `json:"kind"`
	Reason          string `json:"reason"`
	RequestedByName string `json:"requested_by_name"`
	Outcome         string `json:"outcome"`
}

// Campaign is one sending sequence of a pod with its numbers.
type Campaign struct {
	ClientID  string         `json:"client_id"`
	PodSlug   string         `json:"pod_slug"`
	OSKey     string         `json:"os_key"`
	Name      string         `json:"name"`
	Status    string         `json:"status"`
	StartedOn *string        `json:"started_on"`
	Metrics   map[string]int `json:"metrics"`
}

// Prospect is one person in a pod's pipeline, as far as the client needs to see.
type Prospect struct {
	ClientID    string  `json:"client_id"`
	PodSlug     string  `json:"pod_slug"`
	OSID        string  `json:"os_id"`
	Company     string  `json:"company"`
	Person      string  `json:"person"`
	Role        string  `json:"role"`
	Stage       string  `json:"stage"`
	LastTouchAt *string `json:"last_touch_at"`
	NextStep    string  `json:"next_step"`
	Campaign    string  `json:"campaign"`
}

// SupportMessage is a message a client wrote in the portal's Support tab.
type SupportMessage struct {
	ID         string `json:"id"`
	ClientID   string `json:"client_id"`
	Topic      string `json:"topic"`
	Body       string `json:"body"`
	AuthorName string `json:"author_name"`
}

// Result is what one pass brought in that someone at TFG should hear about.
type Result struct {
	Messages []SupportMessage
	Clients  map[string]string // portal client id → client name
}

// Portal is the client portal as the sync needs it.
type Portal interface {
	UpsertPod(ctx context.Context, p Pod) error
	Approvals(ctx context.Context, clientID, podSlug string) ([]Approval, error)
	InsertApproval(ctx context.Context, a Approval) error
	UpdateApproval(ctx context.Context, id string, fields map[string]any) error
	InsertActivity(ctx context.Context, rows []Activity) error
	OpenRequests(ctx context.Context, clientID string) ([]Request, error)
	CloseRequest(ctx context.Context, id, outcome string) error
	UpsertMeetings(ctx context.Context, rows []Meeting) error
	UpsertWeekly(ctx context.Context, rows []Weekly) error
	UpsertCampaigns(ctx context.Context, rows []Campaign) error
	UpsertProspects(ctx context.Context, rows []Prospect) error
	NewSupportMessages(ctx context.Context, clientID string) ([]SupportMessage, error)
	MarkSupportPulled(ctx context.Context, id string) error
	ClientName(ctx context.Context, clientID string) (string, error)
	ClearPod(ctx context.Context, clientID, podSlug string) error
}

// Activity kinds that stay inside TFG (operator plumbing, not client news).
var internalKinds = map[string]bool{"agent_synced": true, "mailbox": true, "pod_created": true, "task_watch": true, "client_message": true}

// Sync runs one pass over every linked pod; one pod's failure does not stop the rest.
func Sync(ctx context.Context, s *pods.Store, portal Portal) (Result, error) {
	res := Result{Clients: map[string]string{}}
	list, err := s.List(ctx)
	if err != nil {
		return res, err
	}
	byClient := map[string][]pods.Pod{}
	var clients []string
	for _, p := range list {
		if p.PortalClientID == nil || *p.PortalClientID == "" {
			continue
		}
		c := *p.PortalClientID
		if _, ok := byClient[c]; !ok {
			clients = append(clients, c)
		}
		byClient[c] = append(byClient[c], p)
	}
	var errs []error
	failed := map[string]error{}
	// 1. Pause and resume first, for every client, each with its own short budget.
	for _, c := range clients {
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := pullRequests(rctx, s, portal, byClient[c], c); err != nil {
			for _, p := range byClient[c] {
				failed[p.Slug] = err
			}
			errs = append(errs, fmt.Errorf("%s requests: %w", c, err))
		}
		cancel()
	}
	// 2. Client messages: logged on the client's first pod and passed back to be announced.
	for _, c := range clients {
		msgs, err := portal.NewSupportMessages(ctx, c)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s messages: %w", c, err))
			continue
		}
		p := byClient[c][0]
		for _, m := range msgs {
			_ = s.Log(ctx, p.ID, "client_message", clip(p.ClientName+" wrote ("+m.Topic+"): "+m.Body, 300), m.ID)
			if err := portal.MarkSupportPulled(ctx, m.ID); err != nil {
				errs = append(errs, err)
				continue
			}
			res.Messages = append(res.Messages, m)
			res.Clients[c] = p.ClientName
		}
	}
	// 3. Everything else, pod by pod; each pod's outcome is kept as its portal health.
	for _, c := range clients {
		for _, p := range byClient[c] {
			err := failed[p.Slug]
			if err == nil {
				err = syncPod(ctx, s, portal, p, c)
			}
			detail := "In step with the client portal."
			if err != nil {
				detail = "The client portal couldn't be read or updated: " + err.Error()
				errs = append(errs, fmt.Errorf("%s: %w", p.Slug, err))
			}
			_ = s.SetHealth(ctx, p.ID, "portal", err == nil, detail)
		}
	}
	return res, errors.Join(errs...)
}

// Relink moves a pod to another portal client: the client must exist, the old client's copy of
// this pod is removed, and the activity history is sent again from the start for the new client.
// It returns the new client's name.
func Relink(ctx context.Context, s *pods.Store, portal Portal, slug, clientID string) (string, error) {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return "", err
	}
	clientID = strings.ToLower(strings.TrimSpace(clientID))
	name := ""
	if clientID != "" {
		if name, err = portal.ClientName(ctx, clientID); err != nil {
			return "", err
		}
	}
	if p.PortalClientID != nil && *p.PortalClientID != "" && *p.PortalClientID != clientID {
		if err := portal.ClearPod(ctx, *p.PortalClientID, p.Slug); err != nil {
			return "", fmt.Errorf("couldn't remove this pod from the previous client's portal: %w", err)
		}
	}
	if err := s.LinkPortal(ctx, slug, clientID); err != nil {
		return "", err
	}
	if p.PortalClientID == nil || *p.PortalClientID != clientID {
		if err := s.ResetPortalCursor(ctx, p.ID); err != nil {
			return "", err
		}
	}
	return name, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func syncPod(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	if err := pullDecisions(ctx, s, portal, p, client); err != nil {
		return err
	}
	p, err := s.Get(ctx, p.Slug)
	if err != nil {
		return err
	}
	missing, err := s.Readiness(ctx, p)
	if err != nil {
		return err
	}
	if missing == nil {
		missing = []string{}
	}
	weekends := ""
	if p.SendWeekends {
		weekends = ", weekends included"
	}
	if err := portal.UpsertPod(ctx, Pod{ClientID: client, PodSlug: p.Slug, Name: p.Slug, SyncedAt: nowString(),
		Status: p.Status, ApprovalPolicy: p.ApprovalPolicy,
		Sender: strings.TrimSpace(p.SenderName + " <" + p.SenderEmail + ">"),
		SendingRules: fmt.Sprintf("Up to %d first emails and %d follow-ups a day, %02d:00-%02d:00 %s%s",
			p.FirstEmailDailyCap, p.FollowUpDailyCap, p.SendWindowStart, p.SendWindowEnd, p.Timezone, weekends),
		Missing: missing, Baseline: baselineOf(ctx, s, p)}); err != nil {
		return err
	}
	for _, step := range []func(context.Context, *pods.Store, Portal, pods.Pod, string) error{
		pushApprovals, pushMeetings, pushWeekly, pushCampaigns, pushProspects, pushActivity} {
		if err := step(ctx, s, portal, p, client); err != nil {
			return err
		}
	}
	return nil
}

func pushApprovals(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	mine, err := s.Approvals(ctx, p.ID)
	if err != nil {
		return err
	}
	theirs, err := portal.Approvals(ctx, client, p.Slug)
	if err != nil {
		return err
	}
	byKey := map[string]Approval{}
	for _, a := range theirs {
		byKey[a.OSKey] = a
	}
	for _, a := range mine {
		key := a.Kind + ":" + a.Ref
		have, ok := byKey[key]
		status := a.Status
		if status != "withdrawn" {
			status = "pending"
			if a.Status == "approved" || a.Status == "changes_requested" {
				status = a.Status
			}
		}
		switch {
		case !ok && a.Status == "withdrawn":
		case !ok:
			if err := portal.InsertApproval(ctx, Approval{ClientID: client, PodSlug: p.Slug, OSKey: key, Kind: a.Kind, Title: a.Title,
				Payload: a.Payload, ContentHash: a.Hash, Status: status}); err != nil {
				return err
			}
		case have.ContentHash != a.Hash || (a.Status == "pending" && have.Status != "pending" && have.DecidedAt == "") ||
			(a.Status == "withdrawn" && have.Status != "withdrawn"):
			if err := portal.UpdateApproval(ctx, have.ID, map[string]any{"title": a.Title, "payload": a.Payload,
				"content_hash": a.Hash, "status": status, "decided_at": nil, "decided_by": nil, "decided_by_name": nil,
				"client_note": "", "pulled_at": nil}); err != nil {
				return err
			}
		}
	}
	return nil
}

func pullDecisions(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	theirs, err := portal.Approvals(ctx, client, p.Slug)
	if err != nil {
		return err
	}
	for _, a := range theirs {
		if a.DecidedAt == "" || a.Pulled || (a.Status != "approved" && a.Status != "changes_requested") {
			continue
		}
		kind, ref, ok := strings.Cut(a.OSKey, ":")
		if !ok {
			continue
		}
		applied, err := s.ApplyClientDecision(ctx, p.ID, kind, ref, a.ContentHash, a.Status, a.DecidedByName, a.ClientNote)
		if err != nil {
			return err
		}
		if applied {
			if err := portal.UpdateApproval(ctx, a.ID, map[string]any{"pulled_at": nowString()}); err != nil {
				return err
			}
			continue
		}
		// The wording changed after the client looked: ask again.
		if err := portal.UpdateApproval(ctx, a.ID, map[string]any{"status": "pending", "decided_at": nil,
			"decided_by": nil, "decided_by_name": nil, "client_note": "", "pulled_at": nil}); err != nil {
			return err
		}
	}
	return nil
}

func pushActivity(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	after, err := s.PortalCursor(ctx, p.ID)
	if err != nil {
		return err
	}
	rows, err := s.ActivityAfter(ctx, p.ID, after, 200)
	if err != nil || len(rows) == 0 {
		return err
	}
	var out []Activity
	for _, r := range rows {
		if !internalKinds[r.Kind] {
			out = append(out, Activity{ClientID: client, PodSlug: p.Slug, OSID: r.ID, At: r.At, Kind: r.Kind, Summary: r.Summary})
		}
	}
	if len(out) > 0 {
		if err := portal.InsertActivity(ctx, out); err != nil {
			return err
		}
	}
	return s.SetPortalCursor(ctx, p.ID, rows[len(rows)-1].ID)
}

func pullRequests(ctx context.Context, s *pods.Store, portal Portal, list []pods.Pod, client string) error {
	reqs, err := portal.OpenRequests(ctx, client)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		who := strings.TrimSpace(r.RequestedByName)
		if who == "" {
			who = "the client"
		}
		var outcomes []string
		for _, p := range list {
			if r.PodSlug != "" && r.PodSlug != p.Slug {
				continue
			}
			if np, err := s.Get(ctx, p.Slug); err == nil {
				p = np
			}
			outcome := applyRequest(ctx, s, p, r, who)
			_ = s.Log(ctx, p.ID, "client_request", who+" asked to "+r.Kind+": "+outcome, r.ID)
			if len(list) > 1 {
				outcome = p.Slug + " " + outcome
			}
			outcomes = append(outcomes, outcome)
		}
		if len(outcomes) == 0 {
			outcomes = []string{"no such AI SDR pod"}
		}
		if err := portal.CloseRequest(ctx, r.ID, strings.Join(outcomes, "; ")); err != nil {
			return err
		}
	}
	return nil
}

func applyRequest(ctx context.Context, s *pods.Store, p pods.Pod, r Request, who string) string {
	switch r.Kind {
	case "pause":
		if p.Status == pods.StatusPaused || p.Status == pods.StatusHandedOver || p.Status == pods.StatusSetup {
			return "nothing to pause (status " + p.Status + ")"
		}
		if _, err := s.SetStatus(ctx, p.Slug, pods.StatusPaused, "requested by "+who+": "+r.Reason); err != nil {
			return "could not pause: " + err.Error()
		}
		return "paused"
	case "resume":
		if p.Status != pods.StatusPaused {
			return "nothing to resume (status " + p.Status + ")"
		}
		if _, err := s.SetStatus(ctx, p.Slug, pods.StatusLive, ""); err != nil {
			return "TFG will resume it once ready: " + err.Error()
		}
		return "resumed"
	}
	return "unknown request"
}

// nowString is a timestamp the portal's REST API accepts; swapped in tests.
var nowString = func() string { return time.Now().UTC().Format(time.RFC3339) }

// calLink is a Cal.com booking's reschedule or cancel page, or "" without a booking uid.
func calLink(kind, uid string) string {
	if uid == "" {
		return ""
	}
	if kind == "reschedule" {
		return "https://cal.com/reschedule/" + url.PathEscape(uid)
	}
	return "https://cal.com/booking/" + url.PathEscape(uid) + "?cancel=true"
}

func pushMeetings(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	rows, err := s.MeetingsForPortal(ctx, p.ID)
	if err != nil || len(rows) == 0 {
		return err
	}
	out := make([]Meeting, 0, len(rows))
	for _, m := range rows {
		var at *string
		if m.StartsAt != nil {
			v := m.StartsAt.UTC().Format(time.RFC3339)
			at = &v
		}
		out = append(out, Meeting{ClientID: client, PodSlug: p.Slug, OSID: m.ID, Title: m.Title, Attendee: m.Attendee, StartsAt: at,
			Brief: m.Brief, Status: m.Status, Outcome: m.Outcome, Coaching: m.Coaching,
			RescheduleURL: calLink("reschedule", m.UID), CancelURL: calLink("cancel", m.UID)})
	}
	return portal.UpsertMeetings(ctx, out)
}

// baselineOf is the client's starting numbers, or nil when never set.
func baselineOf(ctx context.Context, s *pods.Store, p pods.Pod) any {
	b, err := s.GetBaseline(ctx, p.ID)
	if err != nil || b.SetAt == "" {
		return nil
	}
	return b
}

func pushWeekly(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	weeks, err := s.Weeks(ctx, p.ID, 26)
	if err != nil || len(weeks) == 0 {
		return err
	}
	out := make([]Weekly, 0, len(weeks))
	for _, w := range weeks {
		review := w.Review
		if w.Corrected {
			review += pods.CorrectedNote
		}
		out = append(out, Weekly{ClientID: client, PodSlug: p.Slug, WeekStart: w.Start, Metrics: w.M, Review: review})
	}
	return portal.UpsertWeekly(ctx, out)
}

func pushCampaigns(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	rows, err := s.CampaignsForPortal(ctx, p)
	if err != nil || len(rows) == 0 {
		return err
	}
	out := make([]Campaign, 0, len(rows))
	for _, c := range rows {
		var started *string
		if c.StartedOn != "" {
			v := c.StartedOn
			started = &v
		}
		status := "live"
		if p.Status != pods.StatusLive {
			status = p.Status
		}
		out = append(out, Campaign{ClientID: client, PodSlug: p.Slug, OSKey: c.Key, Name: c.Name, Status: status, StartedOn: started,
			Metrics: map[string]int{"sent": c.Sent, "delivered": c.Sent - c.Bounced, "bounced": c.Bounced, "replies": c.Replies,
				"optOuts": c.OptOuts, "meetings": c.Meetings}})
	}
	return portal.UpsertCampaigns(ctx, out)
}

func pushProspects(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	rows, err := s.ProspectsForPortal(ctx, p, 2000)
	if err != nil || len(rows) == 0 {
		return err
	}
	out := make([]Prospect, 0, len(rows))
	for _, r := range rows {
		var at *string
		if r.LastTouch != "" {
			v := r.LastTouch
			at = &v
		}
		out = append(out, Prospect{ClientID: client, PodSlug: p.Slug, OSID: r.ID, Company: r.Company, Person: r.Person, Role: r.Role,
			Stage: r.Stage, LastTouchAt: at, NextStep: r.Next, Campaign: r.Campaign})
	}
	// Pushed in pages so one request never grows too large.
	for i := 0; i < len(out); i += 500 {
		j := i + 500
		if j > len(out) {
			j = len(out)
		}
		if err := portal.UpsertProspects(ctx, out[i:j]); err != nil {
			return err
		}
	}
	return nil
}
