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
	"github.com/rhl/businessos-backend/internal/founderos/connectors/caldotcom"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/fathomcalls"
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
				ctx, cancel = context.WithTimeout(context.Background(), 25*time.Second)
				if k, err := st.TaskBriefs(ctx, podTasksFor(d)); k > 0 || err != nil {
					slog.Info("client pods: call briefs requested", "tasked", k, "err", err)
				}
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 25*time.Second)
				if k, err := st.TaskCoaching(ctx, podTasksFor(d)); k > 0 || err != nil {
					slog.Info("client pods: coaching notes requested", "tasked", k, "err", err)
				}
				cancel()
				if n++; n%4 == 0 {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					if k, err := notifyHandoffs(ctx, d, st); k > 0 || err != nil {
						slog.Info("client pods: hand-offs sent", "sent", k, "err", err)
					}
					if k, err := sendBriefs(ctx, d, st); k > 0 || err != nil {
						slog.Info("client pods: call briefs sent", "sent", k, "err", err)
					}
					if k, err := sendCoaching(ctx, d, st); k > 0 || err != nil {
						slog.Info("client pods: coaching notes sent", "sent", k, "err", err)
					}
					if k, err := sendReviews(ctx, d, st); k > 0 || err != nil {
						slog.Info("client pods: weekly reviews sent", "sent", k, "err", err)
					}
					cancel()
				}
				if n%20 == 0 {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					if k, err := st.TaskWeeklyReviews(ctx, podTasksFor(d), time.Now()); k > 0 || err != nil {
						slog.Info("client pods: weekly reviews requested", "tasked", k, "err", err)
					}
					cancel()
				}
				if n%20 == 0 {
					ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
					if k, err := syncPodCRMs(ctx, d, st); k > 0 || err != nil {
						slog.Info("client pods: CRM sync", "written", k, "err", err)
					}
					cancel()
				}
				if n%10 == 0 {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					if k, err := readPodCalendars(ctx, d, st); k > 0 || err != nil {
						slog.Info("client pods: meetings booked", "new", k, "err", err)
					}
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
					if k, err := readPodRecordings(ctx, d, st); k > 0 || err != nil {
						slog.Info("client pods: call recordings matched", "new", k, "err", err)
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

// sendBriefs emails each finished call brief to the client's rep from TFG's
// own inbox (a notice to the client) and logs it for the portal.
func sendBriefs(ctx context.Context, d *Deps, st *pods.Store) (int, error) {
	list, err := st.BriefsToSend(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	var errs []error
	for _, b := range list {
		if b.To == "" {
			_ = st.MarkBriefSent(ctx, b.ID, "no rep email set")
			continue
		}
		when := ""
		if b.At != nil {
			when = " on " + b.At.Format("Mon 2 Jan 15:04 MST")
		}
		err := commsSourcesFor(d).SendEmail(ctx, email.Reply{To: b.To, Subject: "Call brief: " + b.Name + when,
			Text: "Your call brief for \"" + b.Title + "\" with " + b.Name + when + ".\n\n" + b.Text +
				"\n\nAlso in your portal: https://thefoundersgroup.net/portal\n\nThe Founders Group", RecipientApproved: true})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_ = st.MarkBriefSent(ctx, b.ID, "")
		sent++
	}
	return sent, errors.Join(errs...)
}

// podCalendarKey is the pod's own Cal.com key.
func podCalendarKey(d *Deps, slug string) string {
	return d.Resolver.Resolve(caldotcom.PodKeyName(email.PodKeyPrefix(slug)))
}

// readPodCalendars records new bookings from each pod's own Cal.com account.
func readPodCalendars(ctx context.Context, d *Deps, st *pods.Store) (int, error) {
	list, err := st.List(ctx)
	if err != nil {
		return 0, err
	}
	cal := caldotcom.New(d.Resolver)
	added := 0
	var errs []error
	for _, p := range list {
		if p.CalendarProvider != "calcom" || (p.Status != pods.StatusLive && p.Status != pods.StatusDryRun && p.Status != pods.StatusPaused) {
			continue
		}
		key := podCalendarKey(d, p.Slug)
		if key == "" {
			continue
		}
		var meetings []pods.Meeting
		for _, status := range []string{"upcoming", "past"} {
			bs, err := cal.BookingsWithKey(ctx, key, status)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Slug, err))
				break
			}
			for _, b := range bs {
				m := pods.Meeting{UID: b.UID, Title: b.Title, Start: b.Start, Cancelled: b.Status == "cancelled" || b.Status == "rejected"}
				for _, a := range b.Attendees {
					if !strings.EqualFold(a.Email, p.SenderEmail) {
						m.AttendeeEmail, m.AttendeeName = a.Email, a.Name
						break
					}
				}
				meetings = append(meetings, m)
			}
		}
		k, err := st.RecordMeetings(ctx, p.Slug, meetings)
		added += k
		if err != nil {
			errs = append(errs, err)
		}
	}
	return added, errors.Join(errs...)
}

// sendCoaching emails each finished set of coaching notes to the client's rep.
func sendCoaching(ctx context.Context, d *Deps, st *pods.Store) (int, error) {
	list, err := st.CoachingToSend(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	var errs []error
	for _, b := range list {
		if b.To == "" {
			_ = st.MarkCoachingSent(ctx, b.ID, "no rep email set")
			continue
		}
		err := commsSourcesFor(d).SendEmail(ctx, email.Reply{To: b.To, Subject: "Call coaching: " + b.Name,
			Text: "Coaching notes on your call \"" + b.Title + "\" with " + b.Name + ".\n\n" + b.Text +
				"\n\nAlso in your portal: https://thefoundersgroup.net/portal\n\nThe Founders Group", RecipientApproved: true})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_ = st.MarkCoachingSent(ctx, b.ID, "")
		sent++
	}
	return sent, errors.Join(errs...)
}

// sendReviews emails each finished weekly review to the client's team.
func sendReviews(ctx context.Context, d *Deps, st *pods.Store) (int, error) {
	list, err := st.ReviewsToSend(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	var errs []error
	for _, r := range list {
		if r.To == "" {
			_ = st.MarkReviewSent(ctx, r.ID, "no rep email set")
			continue
		}
		m, err := st.WeekMetrics(ctx, r.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		err = commsSourcesFor(d).SendEmail(ctx, email.Reply{To: r.To, Subject: "Weekly AI SDR review: week of " + r.Week + " (" + r.Client + ")",
			Text: pods.WeeklyText(r, m), RecipientApproved: true})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_ = st.MarkReviewSent(ctx, r.ID, "")
		sent++
	}
	return sent, errors.Join(errs...)
}

// podRecorderKey is the pod's own Fathom key.
func podRecorderKey(d *Deps, slug string) string {
	return d.Resolver.Resolve(fathomcalls.PodKeyName(email.PodKeyPrefix(slug)))
}

// readPodRecordings matches recordings in each pod's own Fathom to its calls.
func readPodRecordings(ctx context.Context, d *Deps, st *pods.Store) (int, error) {
	list, err := st.List(ctx)
	if err != nil {
		return 0, err
	}
	fc := fathomcalls.New(d.Resolver)
	added := 0
	var errs []error
	for _, p := range list {
		if p.Status != pods.StatusLive && p.Status != pods.StatusDryRun && p.Status != pods.StatusPaused {
			continue
		}
		key := podRecorderKey(d, p.Slug)
		if key == "" {
			continue
		}
		ms, err := fc.RecentWithKey(ctx, key, time.Now().Add(-7*24*time.Hour))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Slug, err))
			continue
		}
		recs := make([]pods.Recording, 0, len(ms))
		for _, m := range ms {
			at, err := time.Parse(time.RFC3339Nano, m.At())
			if err != nil {
				continue
			}
			r := pods.Recording{Ref: "fathom:" + m.ID(), Start: at, Transcript: strings.Join(fathomcalls.TranscriptLines(m), "\n")}
			for _, inv := range m.CalendarInvitees {
				if inv.Email != nil {
					r.Invitees = append(r.Invitees, *inv.Email)
				}
			}
			recs = append(recs, r)
		}
		k, err := st.AttachRecordings(ctx, p.Slug, recs)
		added += k
		if err != nil {
			errs = append(errs, err)
		}
	}
	return added, errors.Join(errs...)
}
