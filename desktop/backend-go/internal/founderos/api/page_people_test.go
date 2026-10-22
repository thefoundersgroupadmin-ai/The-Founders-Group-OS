package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush"
	"github.com/rhl/businessos-backend/internal/founderos/people"
	"github.com/rhl/businessos-backend/internal/middleware"
)

// asUser is router() with a signed-in BusinessOS user.
func asUser(t *testing.T, d *Deps, user *middleware.BetterAuthUser) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := func(c *gin.Context) {
		if user == nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set(middleware.UserContextKey, user)
		c.Next()
	}
	Register(r.Group("/api"), d, auth)
	return r
}

func peopleSetup(t *testing.T) {
	t.Helper()
	t.Setenv("FOUNDEROS_PEOPLE_DIR", filepath.Join(t.TempDir(), "people"))
	t.Setenv("FOUNDEROS_OWNER_EMAIL", "owner@example.com,karim@example.com")
	usageNoBoardNames(t)
}

var (
	ownerUser = &middleware.BetterAuthUser{ID: "u-owner", Email: "owner@example.com", Name: "Owner"}
	karim     = &middleware.BetterAuthUser{ID: "u-karim", Email: "Karim@Example.com", Name: "Karim"}
)

func do(t *testing.T, r *gin.Engine, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestATeammateConnectsTheirOwnClaudeAndNeverSeesItAgain(t *testing.T) {
	peopleSetup(t)
	r := asUser(t, &Deps{}, karim)
	w := do(t, r, http.MethodGet, "/api/founderos/pages/ai-accounts", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"owner":false`) || !strings.Contains(w.Body.String(), `"claude":{"connected":false`) {
		t.Fatalf("before: %d %s", w.Code, w.Body)
	}
	if w := do(t, r, http.MethodPut, "/api/founderos/pages/ai-accounts/claude", `{"token":"nope"}`); w.Code != 400 {
		t.Fatalf("bad token: %d", w.Code)
	}
	tok := "sk-ant-oat01-abcdefghijklmnopqrstuvwxyz0123456789"
	w = do(t, r, http.MethodPut, "/api/founderos/pages/ai-accounts/claude", `{"token":"`+tok+`"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), tok) || !strings.Contains(w.Body.String(), `"claude":{"connected":true,"via":"your Claude plan"}`) {
		t.Fatalf("after: %d %s", w.Code, w.Body)
	}
	if people.ClaudeToken("u-karim") != tok {
		t.Fatal("saved under the teammate's own id")
	}
	if w := do(t, r, http.MethodDelete, "/api/founderos/pages/ai-accounts/claude", ""); w.Code != 200 || people.ClaudeToken("u-karim") != "" {
		t.Fatalf("disconnect: %d", w.Code)
	}
}

func TestTheOwnerRunsOnThisMacsLogins(t *testing.T) {
	peopleSetup(t)
	w := do(t, asUser(t, &Deps{}, ownerUser), http.MethodGet, "/api/founderos/pages/ai-accounts", "")
	if !strings.Contains(w.Body.String(), `"owner":true`) || !strings.Contains(w.Body.String(), "this Mac's Claude login") {
		t.Fatalf("%s", w.Body)
	}
}

func TestALaptopPushesOnlyItsOwnersUsageAndTheUsagePageSplitsByPerson(t *testing.T) {
	peopleSetup(t)
	recv := devicepush.NewReceiver(devicepush.NewMemStore())
	d := &Deps{Devices: recv}
	kr := asUser(t, d, karim)

	w := do(t, kr, http.MethodPost, "/api/founderos/pages/ai-accounts/laptop", "", "Origin", "https://os.example.net")
	var issued struct{ Command, Device string }
	_ = json.Unmarshal(w.Body.Bytes(), &issued)
	if w.Code != 200 || !strings.HasPrefix(issued.Command, "curl -fsSL https://os.example.net/api/founderos/laptop/install.sh | TFG_OS_URL=https://os.example.net TFG_LAPTOP_TOKEN=tfgl_") || issued.Device != "laptop-karim" {
		t.Fatalf("issue: %d %s", w.Code, w.Body)
	}
	token := issued.Command[strings.Index(issued.Command, "tfgl_"):strings.LastIndex(issued.Command, " bash")]

	now := time.Now().UTC()
	day := now.In(time.Local).Format("2006-01-02")
	seat := func(id, kind string, in float64) devicepush.SeatUsage {
		return devicepush.SeatUsage{ID: id, Kind: kind, Label: id, CapturedAt: now.Format(time.RFC3339),
			Days: []devicepush.DayBucket{{Day: day, Tot: devicepush.Tot{In: in}}}, ByModel: map[string]devicepush.Tot{}}
	}
	// The laptop claims to be the owner's Mac and sends chats: both ignored.
	sneaky := devicepush.Payload{Device: "owners-mac", Owner: "owner@example.com", CapturedAt: now.Format(time.RFC3339),
		WhatsApp: &devicepush.WhatsAppData{Chats: []devicepush.Chat{{Title: "private"}}},
		Usage:    []devicepush.SeatUsage{seat("claude-owners-mac", "claude", 700)}}
	raw, _ := json.Marshal(sneaky)
	unauth := gin.New()
	Register(unauth.Group("/api"), d)
	if w := do(t, unauth, http.MethodPost, "/api/founderos/laptop/push", string(raw), "Authorization", "Bearer tfgl_wrong"); w.Code != 401 {
		t.Fatalf("bad token: %d", w.Code)
	}
	if w := do(t, unauth, http.MethodPost, "/api/founderos/laptop/push", string(raw), "Authorization", "Bearer "+token); w.Code != 200 {
		t.Fatalf("push: %d %s", w.Code, w.Body)
	}
	seats, _ := recv.UsageSeats(t.Context())
	if len(seats) != 1 || seats[0].Device != "laptop-karim" || seats[0].Owner != "karim@example.com" || seats[0].Data.ID != "claude-laptop-karim" {
		t.Fatalf("stored = %+v", seats)
	}

	// The owner's Mac pushes with the shared device token.
	t.Setenv("FOUNDEROS_DEVICE_TOKEN", "dev-token")
	mac := devicepush.Payload{Device: "owners-mac", Label: "Mac", CapturedAt: now.Format(time.RFC3339), Usage: []devicepush.SeatUsage{seat("claude-owners-mac", "claude", 5000)}}
	raw, _ = json.Marshal(mac)
	if w := do(t, unauth, http.MethodPost, "/api/founderos/device/push", string(raw), "Authorization", "Bearer dev-token"); w.Code != 200 {
		t.Fatalf("mac push: %d %s", w.Code, w.Body)
	}

	machines := func(r *gin.Engine, q string) (string, []string) {
		w := do(t, r, http.MethodGet, "/api/founderos/pages/usage"+q, "")
		var b struct {
			Scope  string
			Claude *struct{ Machines []struct{ ID string } }
		}
		_ = json.Unmarshal(w.Body.Bytes(), &b)
		var ids []string
		if b.Claude != nil {
			for _, m := range b.Claude.Machines {
				ids = append(ids, m.ID)
			}
		}
		return b.Scope, ids
	}
	if s, ids := machines(kr, ""); s != "me" || strings.Join(ids, ",") != "claude-laptop-karim" {
		t.Fatalf("karim's own board: %s %v", s, ids)
	}
	or := asUser(t, d, ownerUser)
	if s, ids := machines(or, ""); s != "me" || strings.Join(ids, ",") != "claude-owners-mac" {
		t.Fatalf("owner's own board: %s %v", s, ids)
	}
	if s, ids := machines(or, "?who=team"); s != "team" || len(ids) != 2 {
		t.Fatalf("team board: %s %v", s, ids)
	}
}

func TestTheInstallerIsPublicButTheReporterNeedsALaptopToken(t *testing.T) {
	peopleSetup(t)
	r := gin.New()
	Register(r.Group("/api"), &Deps{})
	w := do(t, r, http.MethodGet, "/api/founderos/laptop/install.sh", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "-usage-only") || strings.Contains(w.Body.String(), "tfgl_") {
		t.Fatalf("installer: %d", w.Code)
	}
	if w := do(t, r, http.MethodGet, "/api/founderos/laptop/collector/arm64", ""); w.Code != 401 {
		t.Fatalf("reporter without token: %d", w.Code)
	}
}

func TestLaptopPayloadKeepsUsageOnly(t *testing.T) {
	in := devicepush.Payload{Device: "x", Obsidian: &devicepush.ObsidianData{}, WhatsApp: &devicepush.WhatsAppData{},
		Usage: []devicepush.SeatUsage{{ID: "a", Kind: "claude"}, {ID: "b", Kind: "codex"}, {ID: "c", Kind: "other"}}}
	out := LaptopPayload(people.Laptop{Email: "ilya.b@example.com"}, in)
	if out.Device != "laptop-ilya-b" || out.Obsidian != nil || out.WhatsApp != nil || len(out.Usage) != 2 || out.Label != "ilya.b's laptop" {
		t.Fatalf("%+v", out)
	}
}
