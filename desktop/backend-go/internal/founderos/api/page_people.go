package api

// Personal AI logins and laptops (people package): each teammate's AI calls
// spend their own Claude and Codex plans, and their own laptop reports their
// usage. Usage → "My AI accounts" is the page half.
//
//	GET    /pages/ai-accounts                what this person has connected
//	PUT    /pages/ai-accounts/claude         {token} from `claude setup-token`
//	PUT    /pages/ai-accounts/codex          {apiKey} an OpenAI API key
//	POST   /pages/ai-accounts/codex/login    start a ChatGPT sign-in (link + code)
//	POST   /pages/ai-accounts/laptop         new laptop token + the install line
//	DELETE /pages/ai-accounts/:what          claude | codex | laptop
//
// Machine routes, no browser session (a laptop's terminal has none):
//
//	GET  /laptop/install.sh                  the installer (no secrets in it)
//	GET  /laptop/collector/:arch             the usage reporter (laptop token)
//	POST /laptop/push                        usage only (laptop token)

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush"
	"github.com/rhl/businessos-backend/internal/founderos/people"
	"github.com/rhl/businessos-backend/internal/middleware"
)

func init() { RegisterPage(peopleRegister) }

// withPerson puts the signed-in person on the request context, so an AI
// call made for them (shared.ClaudeCommand) runs on their own login.
func withPerson(c *gin.Context) {
	if u := middleware.GetCurrentUser(c); u != nil {
		p := people.Person{ID: u.ID, Email: strings.ToLower(u.Email), Name: u.Name,
			Owner: people.OwnerEmail() == "" || people.IsOwner(u.Email)}
		c.Request = c.Request.WithContext(people.With(c.Request.Context(), p))
	}
	c.Next()
}

func personOf(c *gin.Context) (people.Person, bool) {
	p, ok := people.From(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
	}
	return p, ok
}

type aiAccounts struct {
	Owner  bool `json:"owner"`
	Claude struct {
		Connected bool   `json:"connected"`
		Via       string `json:"via"`
	} `json:"claude"`
	Codex struct {
		Connected bool                   `json:"connected"`
		Via       string                 `json:"via"`
		Login     people.CodexLoginState `json:"login"`
	} `json:"codex"`
	Laptop struct {
		Connected  bool       `json:"connected"`
		Issued     bool       `json:"issued"`
		Device     string     `json:"device"`
		LastPushAt *time.Time `json:"lastPushAt"`
	} `json:"laptop"`
}

func accountsFor(c *gin.Context, d *Deps, p people.Person) aiAccounts {
	var a aiAccounts
	a.Owner = p.Owner
	if p.Owner {
		a.Claude.Connected, a.Claude.Via = true, "this Mac's Claude login"
		a.Codex.Connected, a.Codex.Via = true, "this Mac's Codex login"
	} else {
		if people.ClaudeToken(p.ID) != "" {
			a.Claude.Connected, a.Claude.Via = true, "your Claude plan"
		}
		switch people.CodexKind(p.ID) {
		case "chatgpt":
			a.Codex.Connected, a.Codex.Via = true, "your ChatGPT plan"
		case "api key":
			a.Codex.Connected, a.Codex.Via = true, "your OpenAI API key"
		}
	}
	a.Codex.Login = people.CodexLogin(p.ID)
	a.Laptop.Device = laptopDevice(p.Email)
	if _, ok := people.LaptopFor(p.ID); ok {
		a.Laptop.Issued = true
	}
	if d.Devices != nil {
		if seats, err := d.Devices.UsageSeats(c.Request.Context()); err == nil {
			for _, s := range seats {
				if s.Device == a.Laptop.Device {
					t := s.PushedAt
					a.Laptop.Connected, a.Laptop.LastPushAt = true, &t
				}
			}
		}
	}
	return a
}

func peopleRegister(s *gin.RouterGroup, d *Deps) {
	g := s.Group("/pages/ai-accounts")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	g.GET("", func(c *gin.Context) {
		if p, ok := personOf(c); ok {
			c.JSON(http.StatusOK, accountsFor(c, d, p))
		}
	})
	g.PUT("/claude", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		_ = c.ShouldBindJSON(&body)
		if err := people.SaveClaudeToken(p.ID, body.Token); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, accountsFor(c, d, p))
	})
	g.PUT("/codex", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		var body struct {
			APIKey string `json:"apiKey"`
		}
		_ = c.ShouldBindJSON(&body)
		if err := people.SaveCodexAPIKey(p.ID, body.APIKey); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, accountsFor(c, d, p))
	})
	g.POST("/codex/login", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		if p.Owner {
			c.JSON(http.StatusBadRequest, gin.H{"error": "you already run on this Mac's own Codex login"})
			return
		}
		st, err := people.StartCodexLogin(p)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
	g.POST("/laptop", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		token, err := people.IssueLaptopToken(p, time.Now())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		base := publicBase(c)
		c.JSON(http.StatusOK, gin.H{
			"command": fmt.Sprintf("curl -fsSL %s/api/founderos/laptop/install.sh | TFG_OS_URL=%s TFG_LAPTOP_TOKEN=%s bash", base, base, token),
			"device":  laptopDevice(p.Email),
		})
	})
	g.DELETE("/:what", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		var err error
		switch what := c.Param("what"); what {
		case "claude", "codex":
			err = people.Forget(p.ID, what)
		case "laptop":
			err = people.RevokeLaptop(p.ID)
		default:
			c.JSON(http.StatusNotFound, gin.H{"error": "nothing called " + what})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, accountsFor(c, d, p))
	})
}

// publicBase is the address teammates reach the OS at: the page's own origin
// (the browser sends it), else FOUNDEROS_PUBLIC_URL.
func publicBase(c *gin.Context) string {
	if o := strings.TrimRight(c.GetHeader("Origin"), "/"); strings.HasPrefix(o, "https://") || strings.HasPrefix(o, "http://") {
		return o
	}
	if u := strings.TrimRight(os.Getenv("FOUNDEROS_PUBLIC_URL"), "/"); u != "" {
		return u
	}
	return "https://os.thefoundersgroup.net"
}

var laptopJunk = regexp.MustCompile(`[^a-z0-9]+`)

// laptopDevice is the device id a person's laptop pushes under.
func laptopDevice(email string) string {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	slug := strings.Trim(laptopJunk.ReplaceAllString(local, "-"), "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	if slug == "" {
		slug = "someone"
	}
	return "laptop-" + slug
}

// LaptopBinDir holds the prebuilt reporters (tfg-usage-darwin-arm64/amd64),
// built by `make tfg-host`.
func LaptopBinDir() string {
	if d := os.Getenv("FOUNDEROS_LAPTOP_BIN_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".founderos", "bin", "laptop")
}

func laptopToken(c *gin.Context) (people.Laptop, bool) {
	l, ok := people.LaptopByToken(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unknown laptop token: make a new one in TFG OS → Usage → Connect this laptop"})
	}
	return l, ok
}

func registerLaptop(b *gin.RouterGroup, d *Deps) {
	b.GET("/laptop/install.sh", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(laptopInstaller))
	})
	b.GET("/laptop/collector/:arch", func(c *gin.Context) {
		if _, ok := laptopToken(c); !ok {
			return
		}
		arch := c.Param("arch")
		if arch != "arm64" && arch != "amd64" {
			c.JSON(http.StatusNotFound, gin.H{"error": "no reporter for " + arch})
			return
		}
		path := filepath.Join(LaptopBinDir(), "tfg-usage-darwin-"+arch)
		if _, err := os.Stat(path); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the reporter is not built on the OS host yet: run make tfg-host there"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.FileAttachment(path, "tfg-usage")
	})
	b.POST("/laptop/push", func(c *gin.Context) {
		l, ok := laptopToken(c)
		if !ok {
			return
		}
		var in devicepush.Payload
		if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)).Decode(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "payload is not JSON: " + err.Error()})
			return
		}
		if d.Devices == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no device receiver"})
			return
		}
		if err := d.Devices.Accept(c.Request.Context(), LaptopPayload(l, in)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

// LaptopPayload keeps only a laptop push's Claude and Codex usage, under the
// device, labels and owner its token says: a laptop never reports chats,
// notes, vaults or anything else, and cannot pose as another machine.
func LaptopPayload(l people.Laptop, in devicepush.Payload) devicepush.Payload {
	who := strings.TrimSpace(l.Name)
	if who == "" {
		who, _, _ = strings.Cut(l.Email, "@")
	}
	device := laptopDevice(l.Email)
	out := devicepush.Payload{Device: device, Label: who + "'s laptop", Owner: l.Email, CapturedAt: in.CapturedAt}
	for _, s := range in.Usage {
		switch s.Kind {
		case "claude":
			s.ID, s.Label = "claude-"+device, "Claude · "+who+"'s laptop"
		case "codex":
			s.ID, s.Label = "codex-"+device, "Codex · "+who+"'s laptop"
		default:
			continue
		}
		out.Usage = append(out.Usage, s)
	}
	return out
}

// laptopInstaller sets up the usage reporter on a teammate's Mac: one binary
// and one LaunchAgent, every 10 minutes. It reads only Claude Code and Codex
// usage files and needs TFG_OS_URL and TFG_LAPTOP_TOKEN (the install line
// from Usage → Connect this laptop). Run again to update; --remove undoes it.
const laptopInstaller = `#!/bin/bash
# TFG OS: report this laptop's Claude and Codex usage to the team OS.
set -euo pipefail
LABEL=net.thefoundersgroup.tfg-usage
DIR="$HOME/.tfg-laptop"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
DOMAIN="gui/$(id -u)"

if [ "${1:-}" = "--remove" ]; then
  launchctl bootout "$DOMAIN" "$PLIST" 2>/dev/null || true
  rm -f "$PLIST"; rm -rf "$DIR"
  echo "Removed. This laptop no longer reports usage."; exit 0
fi

: "${TFG_OS_URL:?copy the whole line from TFG OS: Usage, Connect this laptop}"
: "${TFG_LAPTOP_TOKEN:?copy the whole line from TFG OS: Usage, Connect this laptop}"
[ "$(uname -s)" = Darwin ] || { echo "This installer is for macOS."; exit 1; }
case "$(uname -m)" in arm64) ARCH=arm64 ;; x86_64) ARCH=amd64 ;; *) echo "Unsupported Mac: $(uname -m)"; exit 1 ;; esac

mkdir -p "$DIR/logs" "$HOME/Library/LaunchAgents"
chmod 700 "$DIR"
echo "Downloading the usage reporter..."
curl -fsS -H "Authorization: Bearer $TFG_LAPTOP_TOKEN" -o "$DIR/tfg-usage.new" "$TFG_OS_URL/api/founderos/laptop/collector/$ARCH" \
  || { echo "Download failed. Make a new line in TFG OS (Usage, Connect this laptop) and try again."; exit 1; }
chmod 755 "$DIR/tfg-usage.new"
mv -f "$DIR/tfg-usage.new" "$DIR/tfg-usage"

umask 077
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array>
    <string>$DIR/tfg-usage</string><string>-usage-only</string>
    <string>-url</string><string>$TFG_OS_URL/api/founderos/laptop/push</string>
    <string>-deadline</string><string>120s</string>
  </array>
  <key>EnvironmentVariables</key><dict><key>FOUNDEROS_COLLECTOR_TOKEN</key><string>$TFG_LAPTOP_TOKEN</string></dict>
  <key>ProcessType</key><string>Background</string>
  <key>StartInterval</key><integer>600</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>$DIR/logs/out.log</string>
  <key>StandardErrorPath</key><string>$DIR/logs/err.log</string>
</dict></plist>
EOF
chmod 600 "$PLIST"

launchctl bootout "$DOMAIN" "$PLIST" 2>/dev/null || true
for i in 1 2 3 4 5; do launchctl bootstrap "$DOMAIN" "$PLIST" 2>/dev/null && break; sleep 1; done

echo "Sending your first usage reading..."
if FOUNDEROS_COLLECTOR_TOKEN="$TFG_LAPTOP_TOKEN" "$DIR/tfg-usage" -usage-only -url "$TFG_OS_URL/api/founderos/laptop/push" -deadline 120s; then
  echo "Done. This laptop now reports your Claude and Codex usage to TFG OS every 10 minutes."
else
  echo "Installed, but the first reading did not go through. It retries every 10 minutes; see $DIR/logs/err.log."
fi
`
