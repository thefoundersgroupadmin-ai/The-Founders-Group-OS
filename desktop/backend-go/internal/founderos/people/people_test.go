package people

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("FOUNDEROS_PEOPLE_DIR", filepath.Join(t.TempDir(), "people"))
	t.Setenv("FOUNDEROS_OWNER_EMAIL", "Owner@Example.com, mate@example.com")
}

const token = "sk-ant-oat01-abcdefghijklmnopqrstuvwxyz0123456789"

func TestTheFirstOwnerEmailIsWhoseLoginsThisMacHolds(t *testing.T) {
	setup(t)
	if !IsOwner("owner@example.com") || IsOwner("mate@example.com") || IsOwner("") {
		t.Fatal("only the first FOUNDEROS_OWNER_EMAIL is the owner")
	}
}

func TestAClaudeTokenIsKeptPrivateAndBadOnesAreRefused(t *testing.T) {
	setup(t)
	if err := SaveClaudeToken("u1", "sk-ant-api03-not-a-login"); err == nil {
		t.Fatal("an API key is not a setup-token")
	}
	if err := SaveClaudeToken("u1", "  "+token+"\n"); err != nil {
		t.Fatal(err)
	}
	if ClaudeToken("u1") != token {
		t.Fatalf("token = %q", ClaudeToken("u1"))
	}
	fi, err := os.Stat(claudeTokenPath("u1"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, %v", fi.Mode(), err)
	}
	if di, _ := os.Stat(Dir("u1")); di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode())
	}
	if err := Forget("u1", "claude"); err != nil || ClaudeToken("u1") != "" {
		t.Fatal("forget must remove the token")
	}
}

func TestAnIDCannotEscapeThePeopleFolder(t *testing.T) {
	setup(t)
	if d := Dir("../../etc"); filepath.Dir(d) != Root() {
		t.Fatalf("dir = %s", d)
	}
}

func TestATeammateRunsOnTheirOwnLoginsAndNeverTheOwners(t *testing.T) {
	setup(t)
	base := []string{"PATH=/bin", "ANTHROPIC_API_KEY=owner", "CLAUDE_CODE_OAUTH_TOKEN=owner", "CODEX_HOME=/owner/.codex", "OPENAI_API_KEY=owner"}
	owner := Person{ID: "o", Email: "owner@example.com", Owner: true}
	if got := Env(owner, base); !slices.Equal(got, base) {
		t.Fatalf("the owner keeps this Mac's env: %v", got)
	}
	mate := Person{ID: "m", Email: "mate@example.com"}
	got := strings.Join(Env(mate, base), "\n")
	for _, bad := range []string{"=owner", "/owner/.codex"} {
		if strings.Contains(got, bad) {
			t.Fatalf("owner login leaked: %s", got)
		}
	}
	if !strings.Contains(got, "CODEX_HOME="+CodexHome("m")) || !strings.Contains(got, "CLAUDE_CONFIG_DIR="+ClaudeConfigDir("m")) || strings.Contains(got, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("no token yet, own empty dirs: %s", got)
	}
	_ = SaveClaudeToken("m", token)
	if got := strings.Join(Env(mate, base), "\n"); !strings.Contains(got, "CLAUDE_CODE_OAUTH_TOKEN="+token) {
		t.Fatalf("token missing: %s", got)
	}
}

func TestCodexAPIKeyLogin(t *testing.T) {
	setup(t)
	if err := SaveCodexAPIKey("m", "nope"); err == nil {
		t.Fatal("want refusal")
	}
	if err := SaveCodexAPIKey("m", "sk-proj-0123456789abcdefghij"); err != nil || CodexKind("m") != "api key" {
		t.Fatalf("kind %q, %v", CodexKind("m"), err)
	}
	_ = Forget("m", "codex")
	if CodexKind("m") != "" {
		t.Fatal("forget codex")
	}
}

func TestParseDeviceAuthReadsTheLinkAndCode(t *testing.T) {
	out := "Welcome\n1. Open this link\n   \x1b[94mhttps://auth.openai.com/codex/device\x1b[0m\n\n2. Enter this one-time code \x1b[90m(expires in 15 minutes)\x1b[0m\n   \x1b[94mAB12-CD34E\x1b[0m\n"
	link, code := ParseDeviceAuth(out)
	if link != "https://auth.openai.com/codex/device" || code != "AB12-CD34E" {
		t.Fatalf("link %q code %q", link, code)
	}
}

func TestCodexLoginShowsTheCodeAndFinishesWhenApproved(t *testing.T) {
	setup(t)
	fake := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\necho 'Open https://auth.openai.com/codex/device'\necho 'Enter this one-time code'\necho 'WXYZ-1234'\nsleep 0.3\nprintf '{\"tokens\":{\"refresh_token\":\"r\"}}' > \"$CODEX_HOME/auth.json\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := CodexBin
	CodexBin = func() (string, error) { return fake, nil }
	defer func() { CodexBin = old }()

	st, err := StartCodexLogin(Person{ID: "m", Email: "mate@example.com"})
	if err != nil || st.State != "waiting" || st.Code != "WXYZ-1234" || !strings.HasPrefix(st.Link, "https://auth.openai.com") {
		t.Fatalf("start = %+v, %v", st, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for CodexLogin("m").State == "waiting" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if s := CodexLogin("m"); s.State != "done" || CodexKind("m") != "chatgpt" {
		t.Fatalf("after approval = %+v kind %q", s, CodexKind("m"))
	}
}

func TestALaptopTokenIsFoundByItsHashAndReplacedOnReissue(t *testing.T) {
	setup(t)
	p := Person{ID: "m", Email: "Mate@Example.com", Name: "Mate"}
	first, err := IssueLaptopToken(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(laptopPath("m"))
	if strings.Contains(string(raw), first) {
		t.Fatal("only the hash is stored")
	}
	if l, ok := LaptopByToken(first); !ok || l.Email != "mate@example.com" {
		t.Fatalf("lookup = %+v %v", l, ok)
	}
	second, _ := IssueLaptopToken(p, time.Now())
	if _, ok := LaptopByToken(first); ok {
		t.Fatal("the old token must stop working")
	}
	if _, ok := LaptopByToken(second); !ok {
		t.Fatal("new token")
	}
	_ = RevokeLaptop("m")
	if _, ok := LaptopByToken(second); ok {
		t.Fatal("revoked")
	}
}

func TestPersonRidesOnTheContext(t *testing.T) {
	ctx := With(context.Background(), Person{ID: "m"})
	if p, ok := From(ctx); !ok || p.ID != "m" {
		t.Fatal("person")
	}
	if _, ok := From(context.Background()); ok {
		t.Fatal("background work has no person")
	}
}
