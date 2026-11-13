// Package lusha finds a prospect's work email and phone through Lusha's
// search-and-enrich API (LUSHA_API_KEY), the same endpoint the agents' tool
// uses. Each lookup spends Lusha credits, so callers cap how many run a day.
package lusha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
)

const EnvKey = "LUSHA_API_KEY"

var ErrNotConfigured = errors.New("Lusha is not connected: add the API key in Connections")

var Base = "https://api.lusha.com"

type Client struct {
	res  connectors.Resolver
	http *http.Client
}

func New(res connectors.Resolver) *Client {
	return &Client{res: res, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Configured() bool { return strings.TrimSpace(c.res.Resolve(EnvKey)) != "" }

// Person is who to look up. LinkedIn alone is enough; otherwise a name and
// the company's domain.
type Person struct {
	Ref, Name, Domain, LinkedIn string
}

// Found is what Lusha gave back. Empty fields mean it had nothing.
type Found struct {
	Email, Phone string
}

// Find looks one person up.
func (c *Client) Find(ctx context.Context, p Person) (Found, error) {
	key := strings.TrimSpace(c.res.Resolve(EnvKey))
	if key == "" {
		return Found{}, ErrNotConfigured
	}
	contact := map[string]string{"clientReferenceId": p.Ref}
	first, last, _ := strings.Cut(strings.TrimSpace(p.Name), " ")
	if p.LinkedIn != "" {
		contact["linkedinUrl"] = p.LinkedIn
	}
	if first != "" && last != "" && p.Domain != "" {
		contact["firstName"], contact["lastName"], contact["companyDomain"] = first, strings.TrimSpace(last), p.Domain
	}
	if contact["linkedinUrl"] == "" && contact["companyDomain"] == "" {
		return Found{}, errors.New("need a LinkedIn profile, or a full name and the company's domain")
	}
	raw, _ := json.Marshal(map[string]any{"contacts": []any{contact}, "reveal": []string{"emails", "phones"},
		"options": map[string]any{"waterfallEnabled": false}})
	var body []byte
	err := guard.Outbound("lusha.contacts.enrich", func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, Base+"/v3/contacts/search-and-enrich", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("api_key", key)
		res, err := c.http.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		body, _ = io.ReadAll(io.LimitReader(res.Body, 2<<20))
		if res.StatusCode >= 300 {
			var e struct {
				Message string `json:"message"`
				Error   any    `json:"error"`
			}
			_ = json.Unmarshal(body, &e)
			msg := e.Message
			if msg == "" && e.Error != nil {
				msg = fmt.Sprint(e.Error)
			}
			return fmt.Errorf("Lusha answered %d: %s", res.StatusCode, msg)
		}
		return nil
	})
	if err != nil {
		return Found{}, err
	}
	return Parse(body), nil
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[a-z]{2,}$`)

// Parse reads the first work email and the best phone from a Lusha reply,
// whatever nesting the reply uses: it walks every object and takes values
// under email and phone keys.
func Parse(body []byte) Found {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return Found{}
	}
	var emails []string
	var phones []struct{ n, kind string }
	var walk func(x any, key string)
	walk = func(x any, key string) {
		switch t := x.(type) {
		case map[string]any:
			if k := strings.ToLower(key); strings.Contains(k, "phone") {
				num := ""
				for _, f := range []string{"internationalNumber", "number", "phone", "normalizedNumber"} {
					if s, ok := t[f].(string); ok && s != "" {
						num = s
						break
					}
				}
				kind, _ := t["phoneType"].(string)
				if kind == "" {
					kind, _ = t["type"].(string)
				}
				if num != "" {
					phones = append(phones, struct{ n, kind string }{num, strings.ToLower(kind)})
				}
			}
			for k, val := range t {
				walk(val, k)
			}
		case []any:
			for _, e := range t {
				walk(e, key)
			}
		case string:
			k := strings.ToLower(key)
			if strings.Contains(k, "email") && emailRe.MatchString(strings.ToLower(t)) {
				emails = append(emails, strings.ToLower(t))
			}
		}
	}
	walk(v, "")
	var f Found
	if len(emails) > 0 {
		f.Email = emails[0]
	}
	rank := map[string]int{"direct": 3, "work": 3, "mobile": 2}
	best := -1
	for _, p := range phones {
		r := rank[p.kind]
		if r > best {
			best, f.Phone = r, p.n
		}
	}
	return f
}
