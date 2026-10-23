// Package twilio is what the dialer needs from Twilio: browser calling access
// tokens, the one-time setup (an API key, a TwiML app pointing at the OS, the
// caller ID), and checking that a webhook really came from Twilio.
//
// The operator pastes TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN; Setup makes
// the rest and the caller stores it.
package twilio

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rhl/businessos-backend/internal/founderos/connectors"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
)

// Env names.
const (
	EnvAccount   = "TWILIO_ACCOUNT_SID"
	EnvAuth      = "TWILIO_AUTH_TOKEN"
	EnvKeySID    = "TWILIO_API_KEY_SID"
	EnvKeySecret = "TWILIO_API_KEY_SECRET"
	EnvApp       = "TWILIO_TWIML_APP_SID"
	EnvCallerID  = "TWILIO_CALLER_ID"
)

// ErrNotConfigured: no account SID and auth token yet.
var ErrNotConfigured = errors.New("Twilio is not connected: add the Account SID and Auth Token in Connections")

// Base is the REST root; tests point it at a fake.
var Base = "https://api.twilio.com/2010-04-01"

type Client struct {
	res  connectors.Resolver
	http *http.Client
}

func New(res connectors.Resolver) *Client {
	return &Client{res: res, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) get(name string) string { return strings.TrimSpace(c.res.Resolve(name)) }

// Configured: the operator's two values are there.
func (c *Client) Configured() bool { return c.get(EnvAccount) != "" && c.get(EnvAuth) != "" }

// Ready: setup has run too (the browser can be given a token).
func (c *Client) Ready() bool {
	return c.Configured() && c.get(EnvKeySID) != "" && c.get(EnvKeySecret) != "" && c.get(EnvApp) != "" && c.get(EnvCallerID) != ""
}

// CallerID is the number calls show.
func (c *Client) CallerID() string { return c.get(EnvCallerID) }

func (c *Client) call(ctx context.Context, method, path string, form url.Values, out any) error {
	acct, auth := c.get(EnvAccount), c.get(EnvAuth)
	if acct == "" || auth == "" {
		return ErrNotConfigured
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, Base+"/Accounts/"+acct+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(acct, auth)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		if res.StatusCode == http.StatusUnauthorized {
			return errors.New("Twilio refused the Account SID / Auth Token")
		}
		return fmt.Errorf("Twilio answered %d: %s", res.StatusCode, e.Message)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Numbers are the account's phone numbers (E.164).
func (c *Client) Numbers(ctx context.Context) ([]string, error) {
	var body struct {
		Numbers []struct {
			PhoneNumber  string `json:"phone_number"`
			Capabilities struct {
				Voice bool `json:"voice"`
			} `json:"capabilities"`
		} `json:"incoming_phone_numbers"`
	}
	if err := c.call(ctx, http.MethodGet, "/IncomingPhoneNumbers.json?PageSize=50", nil, &body); err != nil {
		return nil, err
	}
	var out []string
	for _, n := range body.Numbers {
		if n.Capabilities.Voice {
			out = append(out, n.PhoneNumber)
		}
	}
	return out, nil
}

// Made is what Setup created, to be stored by the caller.
type Made map[string]string

// Setup makes whatever is missing: an API key for access tokens, a TwiML app
// whose voice URL is voiceURL, and the caller ID (the account's first voice
// number). It never replaces what exists.
func (c *Client) Setup(ctx context.Context, voiceURL string) (Made, error) {
	made := Made{}
	if !c.Configured() {
		return made, ErrNotConfigured
	}
	err := guard.Outbound("twilio.setup", func() error {
		if c.get(EnvKeySID) == "" || c.get(EnvKeySecret) == "" {
			var k struct {
				SID    string `json:"sid"`
				Secret string `json:"secret"`
			}
			if err := c.call(ctx, http.MethodPost, "/Keys.json", url.Values{"FriendlyName": {"TFG OS dialer"}}, &k); err != nil {
				return fmt.Errorf("making an API key: %w", err)
			}
			made[EnvKeySID], made[EnvKeySecret] = k.SID, k.Secret
		}
		if c.get(EnvApp) == "" {
			var a struct {
				SID string `json:"sid"`
			}
			if err := c.call(ctx, http.MethodPost, "/Applications.json", url.Values{
				"FriendlyName": {"TFG OS dialer"}, "VoiceUrl": {voiceURL}, "VoiceMethod": {"POST"},
			}, &a); err != nil {
				return fmt.Errorf("making the calling app: %w", err)
			}
			made[EnvApp] = a.SID
		}
		if c.get(EnvCallerID) == "" {
			nums, err := c.Numbers(ctx)
			if err != nil {
				return err
			}
			if len(nums) == 0 {
				return errors.New("this Twilio account has no phone number yet: buy a US number in Twilio, then run setup again")
			}
			made[EnvCallerID] = nums[0]
		}
		return nil
	})
	return made, err
}

// Token is a Voice access token for one person in the browser (outgoing only).
func (c *Client) Token(identity string, ttl time.Duration, now time.Time) (string, error) {
	if !c.Ready() {
		return "", errors.New("the dialer is not set up yet: run Twilio setup on the Dialer page")
	}
	keySID := c.get(EnvKeySID)
	claims := jwt.MapClaims{
		"jti": fmt.Sprintf("%s-%d", keySID, now.Unix()),
		"iss": keySID,
		"sub": c.get(EnvAccount),
		"nbf": now.Unix(),
		"exp": now.Add(ttl).Unix(),
		"grants": map[string]any{
			"identity": identity,
			"voice": map[string]any{
				"outgoing": map[string]any{"application_sid": c.get(EnvApp)},
				"incoming": map[string]any{"allow": false},
			},
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	t.Header["cty"] = "twilio-fpa;v=1"
	return t.SignedString([]byte(c.get(EnvKeySecret)))
}

// Valid checks X-Twilio-Signature: base64(HMAC-SHA1(auth token, url + each
// POST field name and value, sorted by name)).
func (c *Client) Valid(fullURL string, form url.Values, signature string) bool {
	auth := c.get(EnvAuth)
	if auth == "" || signature == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(Sign(auth, fullURL, form)), []byte(signature)) == 1
}

// Sign is the signature Twilio would send (exported for tests).
func Sign(authToken, fullURL string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(fullURL)
	for _, k := range keys {
		for _, v := range form[k] {
			b.WriteString(k + v)
		}
	}
	m := hmac.New(sha1.New, []byte(authToken))
	m.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// DialTwiML connects the browser leg to the prospect from the caller ID, with
// status callbacks on the phone leg. Values are XML-escaped.
func DialTwiML(callerID, to, statusURL string) string {
	esc := func(s string) string {
		r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
		return r.Replace(s)
	}
	return `<?xml version="1.0" encoding="UTF-8"?><Response><Dial callerId="` + esc(callerID) + `" answerOnBridge="true" timeout="25">` +
		`<Number statusCallback="` + esc(statusURL) + `" statusCallbackMethod="POST" statusCallbackEvent="initiated ringing answered completed">` +
		esc(to) + `</Number></Dial></Response>`
}

// RefuseTwiML ends a call the OS won't place, saying why to the caller.
func RefuseTwiML(why string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return `<?xml version="1.0" encoding="UTF-8"?><Response><Say>` + r.Replace(why) + `</Say><Hangup/></Response>`
}
