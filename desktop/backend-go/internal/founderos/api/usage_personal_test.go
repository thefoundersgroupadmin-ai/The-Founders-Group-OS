package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/people"
)

func TestConnectedTeammateSeesOSUsageWithoutALaptopReporter(t *testing.T) {
	peopleSetup(t)
	if err := people.SaveClaudeToken(karim.ID, "sk-ant-oat01-abcdefghijklmnopqrstuvwxyz0123456789"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(people.ClaudeConfigDir(karim.ID), "projects", "os-request")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","message":{"model":"claude-sonnet-test","usage":{"input_tokens":123,"output_tokens":45,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "request.jsonl"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	r := asUser(t, &Deps{}, karim)
	w := do(t, r, "GET", "/api/founderos/pages/usage", "")
	var body struct {
		Claude *struct {
			ByModel  map[string]struct{ In, Out float64 }
			Machines []struct{ ID string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || body.Claude == nil || body.Claude.ByModel["claude-sonnet-test"].In != 123 || body.Claude.ByModel["claude-sonnet-test"].Out != 45 {
		t.Fatalf("personal OS usage: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "sk-ant-") {
		t.Fatal("token leaked in usage response")
	}
	w = do(t, asUser(t, &Deps{}, ownerUser), "GET", "/api/founderos/pages/usage", "")
	if strings.Contains(w.Body.String(), "claude-sonnet-test") {
		t.Fatal("another person's personal usage leaked into the owner's Me view")
	}
	if w := do(t, r, "DELETE", "/api/founderos/pages/ai-accounts/claude", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = do(t, r, "GET", "/api/founderos/pages/usage", "")
	if strings.Contains(w.Body.String(), "claude-sonnet-test") {
		t.Fatal("disconnected account still published personal host usage")
	}
}
