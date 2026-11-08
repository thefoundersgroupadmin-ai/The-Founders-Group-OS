package api

// Inbound enquiries for client pods (docs/tfg/CLIENT-PODS.md, M3). A client's
// website form posts to /api/founderos/inbound/<token>; no session, the token
// is the proof. JSON or an ordinary HTML form both work, from any website.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

func registerPodInbound(b *gin.RouterGroup, d *Deps) {
	cors := func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
	}
	b.OPTIONS("/inbound/:token", func(c *gin.Context) { cors(c); c.Status(http.StatusNoContent) })
	b.POST("/inbound/:token", func(c *gin.Context) {
		cors(c)
		if d.Pool == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var e pods.Enquiry
		var redirect string
		if strings.HasPrefix(c.ContentType(), "application/json") {
			var in struct {
				Name, Email, Company, Phone, Message, Website string
			}
			if err := c.ShouldBindJSON(&in); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "send name, email, company, phone and message"})
				return
			}
			e = pods.Enquiry{Name: in.Name, Email: in.Email, Company: in.Company, Phone: in.Phone, Message: in.Message, Honeypot: in.Website}
		} else {
			e = pods.Enquiry{Name: c.PostForm("name"), Email: c.PostForm("email"), Company: c.PostForm("company"),
				Phone: c.PostForm("phone"), Message: c.PostForm("message"), Honeypot: c.PostForm("website")}
			if u, err := url.Parse(c.PostForm("_redirect")); err == nil && u.Scheme == "https" {
				redirect = u.String()
			}
		}
		_, err := (&pods.Store{Pool: d.Pool}).Intake(c.Request.Context(), c.Param("token"), e, c.ClientIP())
		switch {
		case errors.Is(err, pods.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown form"})
		case errors.Is(err, pods.ErrInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": "a valid email address is required"})
		case errors.Is(err, pods.ErrRateLimited):
			c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		case err != nil:
			slog.Error("pod inbound", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record the enquiry"})
		case redirect != "":
			c.Redirect(http.StatusSeeOther, redirect)
		default:
			c.JSON(http.StatusAccepted, gin.H{"ok": true, "message": "Thanks, we'll be in touch shortly."})
		}
	})
}

// inboundBase is where client websites reach the OS.
func inboundBase() string {
	if v := strings.TrimRight(os.Getenv("FOUNDEROS_PUBLIC_URL"), "/"); v != "" {
		return v
	}
	return "https://os.thefoundersgroup.net"
}

// InboundSnippet is the HTML form a client pastes into their website.
func InboundSnippet(endpoint string) string {
	return `<form method="post" action="` + endpoint + `">
  <input name="name" placeholder="Name">
  <input name="email" type="email" placeholder="Work email" required>
  <input name="company" placeholder="Company">
  <input name="phone" placeholder="Phone">
  <textarea name="message" placeholder="How can we help?"></textarea>
  <input name="website" style="display:none" tabindex="-1" autocomplete="off">
  <input type="hidden" name="_redirect" value="https://YOUR-SITE/thank-you">
  <button>Send</button>
</form>`
}

var podLoopsOnce sync.Once

// startPodLoops turns new enquiries into AI SDR tasks every 30 seconds and
// emails hand-offs to the client's rep every two minutes. Never under go test.
func startPodLoops(d *Deps) {
	if d.Pool == nil || testing.Testing() {
		return
	}
	podLoopsOnce.Do(func() {
		go func() {
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			n := 0
			for range tick.C {
				st := &pods.Store{Pool: d.Pool}
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				if k, err := st.TaskInbound(ctx, podTasksFor(d)); k > 0 || err != nil {
					slog.Info("client pods: enquiries tasked", "tasked", k, "err", err)
				}
				cancel()
				if n++; n%4 == 0 {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					if k, err := notifyHandoffs(ctx, d, st); k > 0 || err != nil {
						slog.Info("client pods: hand-offs sent", "sent", k, "err", err)
					}
					cancel()
				}
			}
		}()
	})
}

// notifyHandoffs emails each new hand-off to the client's rep from TFG's own
// inbox (a notice to the client, not outreach) and logs it for the portal.
func notifyHandoffs(ctx context.Context, d *Deps, st *pods.Store) (int, error) {
	list, err := st.PendingHandoffs(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	var errs []error
	for _, h := range list {
		if h.To == "" {
			_ = st.MarkHandoff(ctx, h.ID, "no hand-off email set for this client")
			continue
		}
		body := fmt.Sprintf("A buyer is ready for a person at %s.\n\n%s\n\nThe full conversation and research are in your portal: https://thefoundersgroup.net/portal\n\nThe Founders Group", h.Client, h.Summary)
		err := commsSourcesFor(d).SendEmail(ctx, email.Reply{To: h.To, Subject: "Hand-off: a buyer wants to talk (" + h.Client + ")",
			Text: body, RecipientApproved: true})
		if err != nil {
			errs = append(errs, err)
			_ = st.MarkHandoff(ctx, h.ID, err.Error())
			continue
		}
		_ = st.MarkHandoff(ctx, h.ID, "")
		sent++
	}
	return sent, errors.Join(errs...)
}
