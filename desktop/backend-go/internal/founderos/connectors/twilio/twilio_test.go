package twilio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rhl/businessos-backend/internal/founderos/connectors"
)

// Twilio's own documented example (Webhooks security).
func TestSignatureMatchesTwiliosExample(t *testing.T) {
	form := url.Values{"CallSid": {"CA1234567890ABCDE"}, "Caller": {"+12349013030"}, "Digits": {"1234"}, "From": {"+12349013030"}, "To": {"+18005551212"}}
	got := Sign("12345", "https://mycompany.com/myapp.php?foo=1&bar=2", form)
	if got != "0/KCTR6DLpKmkAf8muzZqo1nDgQ=" {
		t.Fatalf("signature %q", got)
	}
}

func TestSetupMakesOnlyWhatIsMissingAndTokensCarryTheApp(t *testing.T) {
	var made []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "AC1" || p != "tok" {
			w.WriteHeader(401)
			return
		}
		_ = r.ParseForm()
		switch {
		case strings.HasSuffix(r.URL.Path, "/Keys.json"):
			made = append(made, "key")
			_, _ = w.Write([]byte(`{"sid":"SK1","secret":"shh"}`))
		case strings.HasSuffix(r.URL.Path, "/Applications.json"):
			made = append(made, "app:"+r.PostForm.Get("VoiceUrl"))
			_, _ = w.Write([]byte(`{"sid":"AP1"}`))
		case strings.HasSuffix(r.URL.Path, "/IncomingPhoneNumbers.json"):
			_, _ = w.Write([]byte(`{"incoming_phone_numbers":[{"phone_number":"+15005550006","capabilities":{"voice":true}}]}`))
		}
	}))
	defer srv.Close()
	old := Base
	Base = srv.URL
	defer func() { Base = old }()
	t.Setenv("FOUNDEROS_WRITES", "1")
	t.Setenv(EnvAccount, "AC1")
	t.Setenv(EnvAuth, "tok")
	c := New(connectors.Resolver{})
	if c.Ready() {
		t.Fatal("ready before setup")
	}
	got, err := c.Setup(context.Background(), "https://os.example/api/founderos/hooks/twilio/voice")
	if err != nil {
		t.Fatal(err)
	}
	if got[EnvKeySID] != "SK1" || got[EnvKeySecret] != "shh" || got[EnvApp] != "AP1" || got[EnvCallerID] != "+15005550006" {
		t.Fatalf("made %v", got)
	}
	if strings.Join(made, ",") != "key,app:https://os.example/api/founderos/hooks/twilio/voice" {
		t.Fatalf("calls %v", made)
	}
	for k, v := range got {
		t.Setenv(k, v)
	}
	if again, _ := c.Setup(context.Background(), "x"); len(again) != 0 {
		t.Fatalf("setup replaced what exists: %v", again)
	}

	now := time.Now()
	tok, err := c.Token("nader_at_x_com", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte("shh"), nil })
	if err != nil || parsed.Header["cty"] != "twilio-fpa;v=1" {
		t.Fatalf("token %v %v", err, parsed.Header)
	}
	cl := parsed.Claims.(jwt.MapClaims)
	g := cl["grants"].(map[string]any)
	out := g["voice"].(map[string]any)["outgoing"].(map[string]any)
	if cl["iss"] != "SK1" || cl["sub"] != "AC1" || g["identity"] != "nader_at_x_com" || out["application_sid"] != "AP1" {
		t.Fatalf("claims %v", cl)
	}
}

func TestDialTwiMLEscapes(t *testing.T) {
	x := DialTwiML("+15005550006", "+12127365000", "https://os.example/s?call=c&x=<1>")
	if !strings.Contains(x, `callerId="+15005550006"`) || !strings.Contains(x, ">+12127365000</Number>") || !strings.Contains(x, "call=c&amp;x=&lt;1&gt;") {
		t.Fatal(x)
	}
}
