package crmsync

// Salesforce (connected app, refresh token): contacts by email; emails and
// notes as completed Tasks, meetings as Events.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const sfVersion = "v59.0"

type Salesforce struct {
	ClientID, ClientSecret, RefreshToken, LoginURL string

	mu       sync.Mutex
	token    string
	instance string
}

func (s *Salesforce) Name() string { return "salesforce" }

func (s *Salesforce) auth(ctx context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" {
		return s.token, s.instance, nil
	}
	login := s.LoginURL
	if login == "" {
		login = "https://login.salesforce.com"
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {s.ClientID}, "client_secret": {s.ClientSecret}, "refresh_token": {s.RefreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(login, "/")+"/services/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return "", "", fmt.Errorf("salesforce login: %d %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		InstanceURL string `json:"instance_url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tok); err != nil {
		return "", "", err
	}
	s.token, s.instance = tok.AccessToken, strings.TrimRight(tok.InstanceURL, "/")
	return s.token, s.instance, nil
}

func (s *Salesforce) req(ctx context.Context, method, path string, body, out any) error {
	tok, inst, err := s.auth(ctx)
	if err != nil {
		return err
	}
	return call(ctx, method, inst+"/services/data/"+sfVersion+path, map[string]string{"Authorization": "Bearer " + tok}, body, out)
}

func soqlQuote(v string) string { return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) }

func (s *Salesforce) UpsertContact(ctx context.Context, c Contact) (string, error) {
	var found struct {
		Records []struct {
			ID string `json:"Id"`
		} `json:"records"`
	}
	q := "SELECT Id FROM Contact WHERE Email = '" + soqlQuote(c.Email) + "' LIMIT 1"
	if err := s.req(ctx, http.MethodGet, "/query?q="+url.QueryEscape(q), nil, &found); err != nil {
		return "", err
	}
	if len(found.Records) > 0 {
		return found.Records[0].ID, nil
	}
	last := c.LastName
	if last == "" {
		last = c.Email
	}
	body := map[string]any{"FirstName": c.FirstName, "LastName": last, "Email": c.Email, "Description": "Added by the AI SDR (" + c.Company + ")"}
	if c.Phone != "" {
		body["Phone"] = c.Phone
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := s.req(ctx, http.MethodPost, "/sobjects/Contact", body, &made); err != nil {
		return "", err
	}
	if made.ID == "" {
		return "", fmt.Errorf("salesforce: no contact id returned")
	}
	return made.ID, nil
}

func (s *Salesforce) task(ctx context.Context, contactID, subtype, subject, body string, at time.Time) error {
	return s.req(ctx, http.MethodPost, "/sobjects/Task", map[string]any{"WhoId": contactID, "Subject": subject, "Description": body,
		"Status": "Completed", "ActivityDate": at.UTC().Format("2006-01-02"), "TaskSubtype": subtype}, nil)
}

func (s *Salesforce) LogEmail(ctx context.Context, contactID string, e Email) error {
	return s.task(ctx, contactID, "Email", "Email: "+e.Subject, e.Body, e.SentAt)
}

func (s *Salesforce) AddNote(ctx context.Context, contactID, text string) error {
	return s.task(ctx, contactID, "Task", "AI SDR note", text, time.Now())
}

func (s *Salesforce) LogMeeting(ctx context.Context, contactID string, m Meeting) error {
	return s.req(ctx, http.MethodPost, "/sobjects/Event", map[string]any{"WhoId": contactID, "Subject": m.Title,
		"StartDateTime": m.StartsAt.UTC().Format(time.RFC3339), "DurationInMinutes": 30, "Description": m.Body}, nil)
}
