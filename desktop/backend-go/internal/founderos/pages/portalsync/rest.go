package portalsync

// REST is the client portal (Supabase PostgREST) reached with the service key
// from PORTAL_SUPABASE_URL / PORTAL_SUPABASE_SERVICE_KEY. Reads go straight
// through; every write runs through guard.Outbound, so FOUNDEROS_WRITES=0
// keeps the portal untouched.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/guard"
)

type REST struct {
	URL, Key string
	HTTP     *http.Client
}

// ErrNotConfigured: no portal URL or service key.
var ErrNotConfigured = errors.New("client portal not configured")

// NewREST reads the portal settings; ok is false when they are missing.
func NewREST(resolve func(string) string) (*REST, bool) {
	u, k := strings.TrimRight(strings.TrimSpace(resolve("PORTAL_SUPABASE_URL")), "/"), strings.TrimSpace(resolve("PORTAL_SUPABASE_SERVICE_KEY"))
	if u == "" || k == "" || !strings.HasPrefix(u, "https://") {
		return nil, false
	}
	return &REST{URL: u, Key: k, HTTP: &http.Client{Timeout: 20 * time.Second}}, true
}

func (r *REST) do(ctx context.Context, method, path string, body any, prefer string, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL+"/rest/v1/"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", r.Key)
	req.Header.Set("Authorization", "Bearer "+r.Key)
	req.Header.Set("Content-Type", "application/json")
	if prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	res, err := r.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("portal %s %s: %d %s", method, strings.SplitN(path, "?", 2)[0], res.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

func (r *REST) write(ctx context.Context, method, path string, body any, prefer string) error {
	return guard.Outbound("portal.sync", func() error { return r.do(ctx, method, path, body, prefer, nil) })
}

func eq(v string) string { return "eq." + url.QueryEscape(v) }

func (r *REST) UpsertPod(ctx context.Context, p Pod) error {
	return r.write(ctx, http.MethodPost, "sdr_pods?on_conflict=pod_slug", p, "resolution=merge-duplicates,return=minimal")
}

// pageSize is how many rows one read asks for; reads keep paging until a short page.
const pageSize = 1000

func (r *REST) Approvals(ctx context.Context, client, podSlug string) ([]Approval, error) {
	var rows []struct {
		Approval
		DecidedAt *string `json:"decided_at"`
		PulledAt  *string `json:"pulled_at"`
	}
	var err error
	for offset := 0; ; offset += pageSize {
		var page []struct {
			Approval
			DecidedAt *string `json:"decided_at"`
			PulledAt  *string `json:"pulled_at"`
		}
		err = r.do(ctx, http.MethodGet, fmt.Sprintf("sdr_approvals?select=id,client_id,pod_slug,os_key,kind,title,content_hash,status,client_note,decided_by_name,decided_at,pulled_at"+
			"&client_id=%s&pod_slug=%s&order=id.asc&limit=%d&offset=%d", eq(client), eq(podSlug), pageSize, offset), nil, "", &page)
		rows = append(rows, page...)
		if err != nil || len(page) < pageSize {
			break
		}
	}
	out := make([]Approval, 0, len(rows))
	for _, x := range rows {
		a := x.Approval
		if x.DecidedAt != nil {
			a.DecidedAt = *x.DecidedAt
		}
		a.Pulled = x.PulledAt != nil
		out = append(out, a)
	}
	return out, err
}

func (r *REST) InsertApproval(ctx context.Context, a Approval) error {
	a.ID = ""
	return r.write(ctx, http.MethodPost, "sdr_approvals", a, "return=minimal")
}

func (r *REST) UpdateApproval(ctx context.Context, id string, fields map[string]any) error {
	fields["updated_at"] = nowString()
	return r.write(ctx, http.MethodPatch, "sdr_approvals?id="+eq(id), fields, "return=minimal")
}

func (r *REST) InsertActivity(ctx context.Context, rows []Activity) error {
	return r.write(ctx, http.MethodPost, "sdr_activity?on_conflict=client_id,os_id", rows, "resolution=ignore-duplicates,return=minimal")
}

func (r *REST) OpenRequests(ctx context.Context, client string) ([]Request, error) {
	var out []Request
	err := r.do(ctx, http.MethodGet, "sdr_requests?select=id,client_id,pod_slug,kind,reason,requested_by_name&handled_at=is.null&order=created_at.asc,id.asc&client_id="+eq(client), nil, "", &out)
	return out, err
}

func (r *REST) CloseRequest(ctx context.Context, id, outcome string) error {
	return r.write(ctx, http.MethodPatch, "sdr_requests?id="+eq(id), map[string]any{"handled_at": nowString(), "confirmed_at": nowString(), "outcome": outcome}, "return=minimal")
}

func (r *REST) UpsertMeetings(ctx context.Context, rows []Meeting) error {
	return r.write(ctx, http.MethodPost, "sdr_meetings?on_conflict=client_id,os_id", rows, "resolution=merge-duplicates,return=minimal")
}

func (r *REST) UpsertWeekly(ctx context.Context, rows []Weekly) error {
	return r.write(ctx, http.MethodPost, "sdr_weekly?on_conflict=client_id,pod_slug,week_start", rows, "resolution=merge-duplicates,return=minimal")
}

func (r *REST) UpsertCampaigns(ctx context.Context, rows []Campaign) error {
	return r.write(ctx, http.MethodPost, "sdr_campaigns?on_conflict=pod_slug,os_key", rows, "resolution=merge-duplicates,return=minimal")
}

func (r *REST) UpsertProspects(ctx context.Context, rows []Prospect) error {
	return r.write(ctx, http.MethodPost, "sdr_prospects?on_conflict=pod_slug,os_id", rows, "resolution=merge-duplicates,return=minimal")
}

func (r *REST) NewSupportMessages(ctx context.Context, client string) ([]SupportMessage, error) {
	var out []SupportMessage
	err := r.do(ctx, http.MethodGet, "support_messages?select=id,client_id,topic,body,author_name&from_client=is.true&pulled_at=is.null&order=created_at.asc&limit=200&client_id="+eq(client), nil, "", &out)
	return out, err
}

func (r *REST) MarkSupportPulled(ctx context.Context, id string) error {
	return r.write(ctx, http.MethodPatch, "support_messages?id="+eq(id), map[string]any{"pulled_at": nowString()}, "return=minimal")
}

// ErrNoSuchClient: the portal has no client with that id.
var ErrNoSuchClient = errors.New("the client portal has no client with that id")

func (r *REST) ClientName(ctx context.Context, client string) (string, error) {
	var out []struct {
		Name string `json:"name"`
	}
	if err := r.do(ctx, http.MethodGet, "clients?select=name&id="+eq(client), nil, "", &out); err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", ErrNoSuchClient
	}
	return out[0].Name, nil
}

// ClearPod removes a pod's rows from a client's portal (when the pod is moved to another client).
func (r *REST) ClearPod(ctx context.Context, client, podSlug string) error {
	for _, t := range []string{"sdr_approvals", "sdr_activity", "sdr_meetings", "sdr_weekly", "sdr_campaigns", "sdr_prospects", "sdr_requests", "sdr_pods"} {
		if err := r.write(ctx, http.MethodDelete, t+"?client_id="+eq(client)+"&pod_slug="+eq(podSlug), nil, "return=minimal"); err != nil {
			return err
		}
	}
	return nil
}

// CreateClient makes the client's record in the portal (as the staff CRM would) and returns its id.
func (r *REST) CreateClient(ctx context.Context, name, pkg string, reps int) (string, error) {
	var out []struct {
		ID string `json:"id"`
	}
	err := guard.Outbound("portal.sync", func() error {
		return r.do(ctx, http.MethodPost, "clients", map[string]any{"name": name, "package": pkg, "reps": reps, "status": "Onboarding",
			"start_date": time.Now().UTC().Format("2006-01-02")}, "return=representation", &out)
	})
	if err != nil {
		return "", err
	}
	if len(out) == 0 || out[0].ID == "" {
		return "", errors.New("the portal didn't return the new client's id")
	}
	return out[0].ID, nil
}

// Access lists who can sign in to a client's portal.
func (r *REST) Access(ctx context.Context, client string) ([]string, error) {
	var out []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := r.do(ctx, http.MethodGet, "client_users?select=name,email&client_id="+eq(client), nil, "", &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out))
	for _, u := range out {
		n := strings.TrimSpace(u.Name)
		if n == "" {
			n = u.Email
		}
		names = append(names, n)
	}
	return names, nil
}
