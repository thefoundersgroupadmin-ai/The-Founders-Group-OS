// Package caldotcom reads Cal.com bookings: who booked a call with TFG and
// when, so a prospect who books moves on in the pipeline and stops getting
// follow-ups. Read-only; it never creates, moves or cancels a booking.
//
// API v2: an API key (cal_live_…, Cal.com → Settings → Developer → API keys) as
// a Bearer header plus the cal-api-version header; GET /bookings?status=…
// (upcoming, past, cancelled, unconfirmed) with cursor pagination.
package caldotcom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
)

var Meta = connectors.Meta{ID: "caldotcom", Name: "Cal.com", Kind: connectors.KindCRM}

// ErrNotConfigured means no CALCOM_API_KEY.
var ErrNotConfigured = errors.New("CALCOM_API_KEY is not set")

const (
	defaultBaseURL = "https://api.cal.com/v2"
	keyName        = "CALCOM_API_KEY"
	apiVersion     = "2026-05-01"
	timeout        = 8 * time.Second
	maxPages       = 5
)

type Attendee struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// Booking is one Cal.com booking reduced to what the pipeline needs. Email is
// trimmed and lowercased so it joins against pipeline contacts.
type Booking struct {
	UID       string     `json:"uid"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Start     string     `json:"start"`
	End       string     `json:"end,omitempty"`
	EventSlug string     `json:"eventSlug,omitempty"`
	Attendees []Attendee `json:"attendees"`
}

type Connector struct {
	res     connectors.Resolver
	baseURL string
	http    *http.Client
}

func New(res connectors.Resolver) *Connector {
	return &Connector{res: res, baseURL: defaultBaseURL, http: connectors.HTTPClient(timeout)}
}

func (c *Connector) key() string { return c.res.Resolve(keyName) }

type page struct {
	Data []struct {
		UID       string `json:"uid"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		Start     string `json:"start"`
		End       string `json:"end"`
		EventType *struct {
			Slug string `json:"slug"`
		} `json:"eventType"`
		Attendees []Attendee `json:"attendees"`
	} `json:"data"`
	Pagination struct {
		NextCursor *string `json:"nextCursor"`
		HasMore    bool    `json:"hasMore"`
	} `json:"pagination"`
}

func parse(body []byte) (page, []Booking) {
	var p page
	if json.Unmarshal(body, &p) != nil {
		return p, []Booking{}
	}
	out := []Booking{}
	for _, d := range p.Data {
		if strings.TrimSpace(d.UID) == "" {
			continue
		}
		b := Booking{UID: d.UID, Title: strings.TrimSpace(d.Title), Status: d.Status, Start: d.Start, End: d.End, Attendees: []Attendee{}}
		if d.EventType != nil {
			b.EventSlug = d.EventType.Slug
		}
		for _, a := range d.Attendees {
			if e := strings.ToLower(strings.TrimSpace(a.Email)); e != "" {
				b.Attendees = append(b.Attendees, Attendee{Name: strings.TrimSpace(a.Name), Email: e})
			}
		}
		out = append(out, b)
	}
	return p, out
}

// ParseBookings reads one GET /bookings page; bookings without a uid are skipped.
func ParseBookings(body []byte) []Booking {
	_, out := parse(body)
	return out
}

func (c *Connector) get(ctx context.Context, key, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("cal-api-version", apiVersion)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Cal.com %s: HTTP %d", path, resp.StatusCode)
	}
	return body, nil
}

// Bookings lists bookings with one status (upcoming, past, cancelled or
// unconfirmed), following the cursor for up to five pages of 100.
func (c *Connector) Bookings(ctx context.Context, status string) ([]Booking, error) {
	key := c.key()
	if key == "" {
		return nil, ErrNotConfigured
	}
	out := []Booking{}
	cursor := ""
	for i := 0; i < maxPages; i++ {
		q := url.Values{"status": {status}, "limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		body, err := c.get(ctx, key, "/bookings?"+q.Encode())
		if err != nil {
			return out, err
		}
		p, list := parse(body)
		out = append(out, list...)
		if !p.Pagination.HasMore || p.Pagination.NextCursor == nil || *p.Pagination.NextCursor == "" {
			break
		}
		cursor = *p.Pagination.NextCursor
	}
	return out, nil
}

func (c *Connector) st(state connectors.State, detail string, meta map[string]any) connectors.Status {
	return connectors.Status{ID: Meta.ID, Name: Meta.Name, Kind: Meta.Kind, State: state, Detail: detail, Meta: meta}
}

// Status reads the upcoming bookings to prove the key works. It never writes.
func (c *Connector) Status(ctx context.Context) connectors.Status {
	key := c.key()
	if key == "" {
		return c.st(connectors.StateNotConfigured,
			"Set CALCOM_API_KEY to see booked calls and stop follow-ups to anyone who books. Create a key in Cal.com → Settings → Developer → API keys.", nil)
	}
	body, err := c.get(ctx, key, "/bookings?status=upcoming&limit=100")
	if err != nil {
		return c.st(connectors.StateError, "CALCOM_API_KEY is set but the call failed: "+err.Error(), nil)
	}
	n := len(ParseBookings(body))
	plural := "s"
	if n == 1 {
		plural = ""
	}
	return c.st(connectors.StateConnected, fmt.Sprintf("Cal.com reachable · %d upcoming booking%s", n, plural),
		map[string]any{"upcoming": n})
}
