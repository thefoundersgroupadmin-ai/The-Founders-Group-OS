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
	return r.write(ctx, http.MethodPost, "sdr_pods?on_conflict=client_id", p, "resolution=merge-duplicates,return=minimal")
}

func (r *REST) Approvals(ctx context.Context, client string) ([]Approval, error) {
	var rows []struct {
		Approval
		DecidedAt *string `json:"decided_at"`
		PulledAt  *string `json:"pulled_at"`
	}
	err := r.do(ctx, http.MethodGet, "sdr_approvals?select=id,client_id,os_key,kind,title,content_hash,status,client_note,decided_by_name,decided_at,pulled_at&client_id="+eq(client), nil, "", &rows)
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
	err := r.do(ctx, http.MethodGet, "sdr_requests?select=id,client_id,kind,reason,requested_by_name&handled_at=is.null&order=created_at.asc,id.asc&client_id="+eq(client), nil, "", &out)
	return out, err
}

func (r *REST) CloseRequest(ctx context.Context, id, outcome string) error {
	return r.write(ctx, http.MethodPatch, "sdr_requests?id="+eq(id), map[string]any{"handled_at": nowString(), "outcome": outcome}, "return=minimal")
}

func (r *REST) UpsertMeetings(ctx context.Context, rows []Meeting) error {
	return r.write(ctx, http.MethodPost, "sdr_meetings?on_conflict=client_id,os_id", rows, "resolution=merge-duplicates,return=minimal")
}
