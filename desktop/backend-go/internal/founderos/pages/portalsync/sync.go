// Package portalsync keeps each client pod and its client's portal in step
// (docs/tfg/CLIENT-PODS.md, M2). TFG OS is the source of truth for the AI
// SDR; the portal is where the client sees it and decides. Each pass, for
// every pod linked to a portal client:
//
//   - push: the pod's state (status, sender, sending rules, what is missing),
//     its approval requests (re-opened in the portal whenever the wording
//     changed) and new activity;
//   - pull: the client's decisions, applied only to the wording they saw (a
//     decision on older wording goes back to pending), and pause/resume
//     requests (pause applies at once; resume only if the pod is ready).
package portalsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

type Pod struct {
	ClientID       string   `json:"client_id"`
	PodSlug        string   `json:"pod_slug"`
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
	OSID     int64  `json:"os_id"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Summary  string `json:"summary"`
}

type Meeting struct {
	ClientID string  `json:"client_id"`
	OSID     int64   `json:"os_id"`
	Title    string  `json:"title"`
	Attendee string  `json:"attendee"`
	StartsAt *string `json:"starts_at"`
	Brief    string  `json:"brief"`
	Status   string  `json:"status"`
	Outcome  string  `json:"outcome"`
	Coaching string  `json:"coaching"`
}

// Weekly is one week of results and its review, as the portal shows it.
type Weekly struct {
	ClientID  string       `json:"client_id"`
	WeekStart string       `json:"week_start"`
	Metrics   pods.Metrics `json:"metrics"`
	Review    string       `json:"review"`
}

type Request struct {
	ID              string `json:"id"`
	ClientID        string `json:"client_id"`
	Kind            string `json:"kind"`
	Reason          string `json:"reason"`
	RequestedByName string `json:"requested_by_name"`
	Outcome         string `json:"outcome"`
}

// Portal is the client portal as the sync needs it.
type Portal interface {
	UpsertPod(ctx context.Context, p Pod) error
	Approvals(ctx context.Context, clientID string) ([]Approval, error)
	InsertApproval(ctx context.Context, a Approval) error
	UpdateApproval(ctx context.Context, id string, fields map[string]any) error
	InsertActivity(ctx context.Context, rows []Activity) error
	OpenRequests(ctx context.Context, clientID string) ([]Request, error)
	CloseRequest(ctx context.Context, id, outcome string) error
	UpsertMeetings(ctx context.Context, rows []Meeting) error
	UpsertWeekly(ctx context.Context, rows []Weekly) error
}

// Activity kinds that stay inside TFG (operator plumbing, not client news).
var internalKinds = map[string]bool{"agent_synced": true, "mailbox": true, "pod_created": true}

// Sync runs one pass over every linked pod; one pod's failure does not stop the rest.
func Sync(ctx context.Context, s *pods.Store, portal Portal) error {
	list, err := s.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range list {
		if p.PortalClientID == nil || *p.PortalClientID == "" {
			continue
		}
		if err := syncPod(ctx, s, portal, p, *p.PortalClientID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Slug, err))
		}
	}
	return errors.Join(errs...)
}

func syncPod(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	// Pull first, so pushed state reflects what the client just did.
	if err := pullRequests(ctx, s, portal, p, client); err != nil {
		return err
	}
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
	if err := portal.UpsertPod(ctx, Pod{ClientID: client, PodSlug: p.Slug, Status: p.Status, ApprovalPolicy: p.ApprovalPolicy,
		Sender: strings.TrimSpace(p.SenderName + " <" + p.SenderEmail + ">"),
		SendingRules: fmt.Sprintf("Up to %d first emails and %d follow-ups a day, %02d:00-%02d:00 %s%s",
			p.FirstEmailDailyCap, p.FollowUpDailyCap, p.SendWindowStart, p.SendWindowEnd, p.Timezone, weekends),
		Missing: missing, Baseline: baselineOf(ctx, s, p)}); err != nil {
		return err
	}
	if err := pushApprovals(ctx, s, portal, p, client); err != nil {
		return err
	}
	if err := pushMeetings(ctx, s, portal, p, client); err != nil {
		return err
	}
	if err := pushWeekly(ctx, s, portal, p, client); err != nil {
		return err
	}
	return pushActivity(ctx, s, portal, p, client)
}

func pushApprovals(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	mine, err := s.Approvals(ctx, p.ID)
	if err != nil {
		return err
	}
	theirs, err := portal.Approvals(ctx, client)
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
			if err := portal.InsertApproval(ctx, Approval{ClientID: client, OSKey: key, Kind: a.Kind, Title: a.Title,
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
	theirs, err := portal.Approvals(ctx, client)
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
			out = append(out, Activity{ClientID: client, OSID: r.ID, At: r.At, Kind: r.Kind, Summary: r.Summary})
		}
	}
	if len(out) > 0 {
		if err := portal.InsertActivity(ctx, out); err != nil {
			return err
		}
	}
	return s.SetPortalCursor(ctx, p.ID, rows[len(rows)-1].ID)
}

func pullRequests(ctx context.Context, s *pods.Store, portal Portal, p pods.Pod, client string) error {
	reqs, err := portal.OpenRequests(ctx, client)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		who := strings.TrimSpace(r.RequestedByName)
		if who == "" {
			who = "the client"
		}
		var outcome string
		switch r.Kind {
		case "pause":
			if p.Status == pods.StatusPaused || p.Status == pods.StatusHandedOver || p.Status == pods.StatusSetup {
				outcome = "nothing to pause (status " + p.Status + ")"
				break
			}
			if _, err := s.SetStatus(ctx, p.Slug, pods.StatusPaused, "requested by "+who+": "+r.Reason); err != nil {
				outcome = "could not pause: " + err.Error()
			} else {
				outcome = "paused"
			}
		case "resume":
			if p.Status != pods.StatusPaused {
				outcome = "nothing to resume (status " + p.Status + ")"
				break
			}
			if _, err := s.SetStatus(ctx, p.Slug, pods.StatusLive, ""); err != nil {
				outcome = "TFG will resume it once ready: " + err.Error()
			} else {
				outcome = "resumed"
			}
		default:
			outcome = "unknown request"
		}
		if err := portal.CloseRequest(ctx, r.ID, outcome); err != nil {
			return err
		}
		_ = s.Log(ctx, p.ID, "client_request", who+" asked to "+r.Kind+": "+outcome, r.ID)
		if np, err := s.Get(ctx, p.Slug); err == nil {
			p = np
		}
	}
	return nil
}

// nowString is a timestamp the portal's REST API accepts; swapped in tests.
var nowString = func() string { return time.Now().UTC().Format(time.RFC3339) }

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
		out = append(out, Meeting{ClientID: client, OSID: m.ID, Title: m.Title, Attendee: m.Attendee, StartsAt: at, Brief: m.Brief, Status: m.Status,
			Outcome: m.Outcome, Coaching: m.Coaching})
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
		out = append(out, Weekly{ClientID: client, WeekStart: w.Start, Metrics: w.M, Review: w.Review})
	}
	return portal.UpsertWeekly(ctx, out)
}
