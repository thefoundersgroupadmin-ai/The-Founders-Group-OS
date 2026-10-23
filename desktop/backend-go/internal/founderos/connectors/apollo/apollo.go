// Package apollo finds a prospect's phone number through Apollo's People
// Match (APOLLO_API_KEY). Apollo reveals numbers asynchronously: the match
// returns Apollo's person id at once and later POSTs the numbers to the
// webhook URL given, which the OS matches back by that id.
package apollo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
)

const EnvKey = "APOLLO_API_KEY"

// ErrNotConfigured: no Apollo key.
var ErrNotConfigured = errors.New("Apollo is not connected: add the API key in Connections")

var Base = "https://api.apollo.io/api/v1"

type Client struct {
	res  connectors.Resolver
	http *http.Client
}

func New(res connectors.Resolver) *Client {
	return &Client{res: res, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Configured() bool { return strings.TrimSpace(c.res.Resolve(EnvKey)) != "" }

// Person is who to look up.
type Person struct {
	Name, Email, Company, Domain, LinkedIn string
}

// Match asks Apollo for a person and their phone; it returns Apollo's id and
// any number it already gave back (some come at once).
func (c *Client) Match(ctx context.Context, p Person, webhookURL string) (id string, phones []Phone, err error) {
	key := strings.TrimSpace(c.res.Resolve(EnvKey))
	if key == "" {
		return "", nil, ErrNotConfigured
	}
	body := map[string]any{"reveal_phone_number": true, "webhook_url": webhookURL}
	first, last, _ := strings.Cut(strings.TrimSpace(p.Name), " ")
	for k, v := range map[string]string{"first_name": first, "last_name": last, "name": p.Name, "email": p.Email,
		"organization_name": p.Company, "domain": p.Domain, "linkedin_url": p.LinkedIn} {
		if strings.TrimSpace(v) != "" {
			body[k] = strings.TrimSpace(v)
		}
	}
	raw, _ := json.Marshal(body)
	var out struct {
		Person *struct {
			ID           string  `json:"id"`
			PhoneNumbers []Phone `json:"phone_numbers"`
		} `json:"person"`
	}
	err = guard.Outbound("apollo.people.match", func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, Base+"/people/match", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set("x-api-key", key)
		res, err := c.http.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
		if res.StatusCode >= 300 {
			var e struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(b, &e)
			msg := e.Error
			if msg == "" {
				msg = e.Message
			}
			return fmt.Errorf("Apollo answered %d: %s", res.StatusCode, msg)
		}
		return json.Unmarshal(b, &out)
	})
	if err != nil {
		return "", nil, err
	}
	if out.Person == nil {
		return "", nil, nil // no match
	}
	return out.Person.ID, out.Person.PhoneNumbers, nil
}

// Phone is one number Apollo has for a person.
type Phone struct {
	Raw       string `json:"raw_number"`
	Sanitized string `json:"sanitized_number"`
	Type      string `json:"type_cd"` // mobile, work_direct, work_hq, ...
	Status    string `json:"status_cd"`
}

// Best picks the number to dial: a direct line, then a mobile, never a
// company switchboard or one Apollo marked invalid.
func Best(phones []Phone) string {
	rank := map[string]int{"work_direct": 3, "mobile": 2, "other": 1}
	best, score := "", 0
	for _, p := range phones {
		n := p.Sanitized
		if n == "" {
			n = p.Raw
		}
		if n == "" || strings.Contains(strings.ToLower(p.Status), "invalid") {
			continue
		}
		if s := rank[strings.ToLower(p.Type)]; s > score {
			best, score = n, s
		}
	}
	return best
}

// Webhook is the body Apollo POSTs once numbers are revealed.
type Webhook struct {
	People []struct {
		ID           string  `json:"id"`
		PhoneNumbers []Phone `json:"phone_numbers"`
	} `json:"people"`
}
