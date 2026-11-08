package crmsync

// Pipedrive (API token): persons by email; emails and notes as notes,
// meetings as activities of type meeting.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

type Pipedrive struct {
	Token, Base string
}

func (p Pipedrive) Name() string { return "pipedrive" }

func (p Pipedrive) url(path string, q url.Values) string {
	base := p.Base
	if base == "" {
		base = "https://api.pipedrive.com/v1"
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("api_token", p.Token)
	return base + path + "?" + q.Encode()
}

func (p Pipedrive) UpsertContact(ctx context.Context, c Contact) (string, error) {
	var found struct {
		Data struct {
			Items []struct {
				Item struct {
					ID int64 `json:"id"`
				} `json:"item"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := call(ctx, http.MethodGet, p.url("/persons/search", url.Values{"term": {c.Email}, "fields": {"email"}, "exact_match": {"true"}}), nil, nil, &found); err != nil {
		return "", err
	}
	if len(found.Data.Items) > 0 {
		return strconv.FormatInt(found.Data.Items[0].Item.ID, 10), nil
	}
	name := (c.FirstName + " " + c.LastName)
	if name == " " {
		name = c.Email
	}
	var made struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	body := map[string]any{"name": name, "email": []any{map[string]any{"value": c.Email, "primary": true}}}
	if c.Phone != "" {
		body["phone"] = []any{map[string]any{"value": c.Phone, "primary": true}}
	}
	if err := call(ctx, http.MethodPost, p.url("/persons", nil), nil, body, &made); err != nil {
		return "", err
	}
	if made.Data.ID == 0 {
		return "", fmt.Errorf("pipedrive: no person id returned")
	}
	return strconv.FormatInt(made.Data.ID, 10), nil
}

func (p Pipedrive) personID(id string) any {
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		return n
	}
	return id
}

func (p Pipedrive) AddNote(ctx context.Context, contactID, text string) error {
	return call(ctx, http.MethodPost, p.url("/notes", nil), nil, map[string]any{"content": text, "person_id": p.personID(contactID)}, nil)
}

func (p Pipedrive) LogEmail(ctx context.Context, contactID string, e Email) error {
	return p.AddNote(ctx, contactID, "Email sent by the AI SDR ("+e.SentAt.UTC().Format("2006-01-02 15:04")+" UTC)\nSubject: "+e.Subject+"\n\n"+e.Body)
}

func (p Pipedrive) LogMeeting(ctx context.Context, contactID string, m Meeting) error {
	t := m.StartsAt.UTC()
	return call(ctx, http.MethodPost, p.url("/activities", nil), nil, map[string]any{"subject": m.Title, "type": "meeting",
		"due_date": t.Format("2006-01-02"), "due_time": t.Format("15:04"), "duration": "00:30",
		"person_id": p.personID(contactID), "note": m.Body}, nil)
}
