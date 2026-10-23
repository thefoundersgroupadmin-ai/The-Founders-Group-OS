package api

// /os/dialer: ring prospects back to back. The browser holds the call
// (Twilio Voice SDK, a short-lived token per person); the OS picks who is
// next, writes a 30-second brief, records how each call ended, moves the
// pipeline, and drafts the follow-up into the Outbox. Twilio and Apollo
// call back through public hooks (dialer_hooks.go).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/apollo"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/twilio"
	"github.com/rhl/businessos-backend/internal/founderos/pages/dialer"
	"github.com/rhl/businessos-backend/internal/founderos/pages/integrations"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

func init() { RegisterPage(registerDialer) }

// hookBase is the public origin Twilio and Apollo reach.
func hookBase() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("FOUNDEROS_PUBLIC_URL")), "/"); u != "" {
		return u
	}
	return "https://os.thefoundersgroup.net"
}

const (
	hookVoicePath  = "/api/founderos/hooks/twilio/voice"
	hookStatusPath = "/api/founderos/hooks/twilio/status"
	hookApolloPath = "/api/founderos/hooks/apollo"
)

// hookSecret guards the Apollo hook; made once, kept in the env file.
func hookSecret(d *Deps) string {
	if s := strings.TrimSpace(d.Resolver.Resolve("FOUNDEROS_HOOK_SECRET")); s != "" {
		return s
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	s := hex.EncodeToString(b)
	path := d.Resolver.Planted
	if path == "" {
		path = d.Resolver.EnvLocal
	}
	if path != "" {
		_ = integrations.UpsertEnv(path, map[string]string{"FOUNDEROS_HOOK_SECRET": s})
	}
	return s
}

var identityJunk = regexp.MustCompile(`[^A-Za-z0-9_]`)

// callerIdentity is the Twilio client identity for a person (letters, digits, _).
func callerIdentity(email string) string {
	return identityJunk.ReplaceAllString(strings.ReplaceAll(strings.ToLower(email), "@", "_at_"), "_")
}

var twilioFor = func(d *Deps) *twilio.Client { return twilio.New(d.Resolver) }
var apolloFor = func(d *Deps) *apollo.Client { return apollo.New(d.Resolver) }

func dialerWS(ctx context.Context, d *Deps) (string, error) {
	var id string
	err := d.Pool.QueryRow(ctx, `SELECT id::text FROM workspaces WHERE slug = $1`, outboxWorkspace).Scan(&id)
	return id, err
}

// Where "today" starts for a caller's stats.
func statsZone() *time.Location {
	if loc, err := time.LoadLocation("Europe/London"); err == nil {
		return loc
	}
	return time.UTC
}

func registerDialer(s *gin.RouterGroup, d *Deps) {
	s.GET("/pages/dialer", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok || !needPool(c, d) {
			return
		}
		ctx := c.Request.Context()
		now := time.Now()
		tw := twilioFor(d)
		out := gin.H{
			"twilio": gin.H{"configured": tw.Configured(), "ready": tw.Ready(), "callerId": tw.CallerID()},
			"apollo": gin.H{"configured": apolloFor(d).Configured()},
		}
		q, err := dialer.Queue(ctx, d.Pool, p.Email, now, 25)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out["queue"] = q
		st, _ := dialer.Today(ctx, d.Pool, p.Email, now, statsZone())
		out["stats"] = st
		var withPhone, noPhone int
		_ = d.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE COALESCE(phone, '') <> ''), count(*) FILTER (WHERE COALESCE(phone, '') = '')
			FROM founderos_funnel_contacts WHERE status IN ('first_touch', 'engaged', 'nurtured')`).Scan(&withPhone, &noPhone)
		out["contacts"] = gin.H{"withPhone": withPhone, "noPhone": noPhone}
		c.JSON(http.StatusOK, out)
	})

	// One-time Twilio setup: API key, calling app, caller ID.
	s.POST("/pages/dialer/setup", func(c *gin.Context) {
		path, ok := integrationsEnvLocal(c, d)
		if !ok {
			return
		}
		made, err := twilioFor(d).Setup(c.Request.Context(), hookBase()+hookVoicePath)
		if len(made) > 0 {
			if werr := integrations.UpsertEnv(path, map[string]string(made)); werr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": werr.Error()})
				return
			}
		}
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		tw := twilioFor(d)
		c.JSON(http.StatusOK, gin.H{"ready": tw.Ready(), "callerId": tw.CallerID()})
	})

	s.GET("/pages/dialer/token", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		tok, err := twilioFor(d).Token(callerIdentity(p.Email), time.Hour, time.Now())
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": tok, "identity": callerIdentity(p.Email), "ttlSeconds": 3600})
	})

	// Next: hold the best prospect for this caller (optionally letting a
	// skipped one go first) and give its brief.
	s.POST("/pages/dialer/next", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok || !needPool(c, d) {
			return
		}
		var body struct {
			Skip string `json:"skip"`
		}
		_ = c.ShouldBindJSON(&body)
		ctx := c.Request.Context()
		now := time.Now()
		if body.Skip != "" {
			_ = dialer.Release(ctx, d.Pool, body.Skip, p.Email)
			// a skip comes back tomorrow, not in a minute
			_, _ = d.Pool.Exec(ctx, `UPDATE founderos_dial_state SET next_call_at = $2 WHERE contact_id = $1`, body.Skip, now.Add(24*time.Hour))
		}
		q, err := dialer.Queue(ctx, d.Pool, p.Email, now, 10)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, pr := range q {
			if pr.ContactID == body.Skip {
				continue
			}
			if err := dialer.Claim(ctx, d.Pool, pr.ContactID, p.Email, now); err != nil {
				continue // a teammate took it a moment ago
			}
			if pr.Brief == "" {
				pr.Brief = dialerBrief(ctx, d, pr)
				_ = dialer.SetBrief(ctx, d.Pool, pr.ContactID, pr.Brief)
			}
			c.JSON(http.StatusOK, gin.H{"prospect": pr})
			return
		}
		c.JSON(http.StatusOK, gin.H{"prospect": nil, "why": "nobody to call right now: everyone is called, claimed, or outside their hours"})
	})

	s.POST("/pages/dialer/calls", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok || !needPool(c, d) {
			return
		}
		var body struct {
			ContactID string `json:"contactId"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.ContactID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "who to call?"})
			return
		}
		if !twilioFor(d).Ready() {
			c.JSON(http.StatusConflict, gin.H{"error": "the dialer is not set up yet"})
			return
		}
		ws, err := dialerWS(c.Request.Context(), d)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		call, err := dialer.Start(c.Request.Context(), d.Pool, ws, body.ContactID, p.Email, time.Now())
		if err != nil {
			status := http.StatusConflict
			if !errors.Is(err, dialer.ErrDNC) && !errors.Is(err, dialer.ErrTaken) && !strings.Contains(err.Error(), "hours") {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"call": call})
	})

	s.POST("/pages/dialer/calls/:id/outcome", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok || !needPool(c, d) {
			return
		}
		var body struct {
			Outcome    string     `json:"outcome"`
			Notes      string     `json:"notes"`
			CallbackAt *time.Time `json:"callbackAt"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		call, err := dialer.Log(c.Request.Context(), d.Pool, c.Param("id"), p.Email, body.Outcome, body.Notes, body.CallbackAt, time.Now())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"call": call})
	})

	// The follow-up email for a call, drafted into the Outbox for a click.
	s.POST("/pages/dialer/calls/:id/followup", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok || !needPool(c, d) {
			return
		}
		if why := aiPaused(); why != "" {
			c.JSON(http.StatusConflict, gin.H{"error": "AI work is paused: " + why})
			return
		}
		ctx := c.Request.Context()
		call, err := dialer.Get(ctx, d.Pool, c.Param("id"))
		if err != nil || call.Caller != p.Email {
			c.JSON(http.StatusNotFound, gin.H{"error": "not your call, or no such call"})
			return
		}
		id, err := draftCallFollowUp(ctx, d, call, p)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"outboxId": id})
	})

	s.POST("/pages/dialer/dnc", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok || !needPool(c, d) {
			return
		}
		var body struct {
			Number string `json:"number"`
			Reason string `json:"reason"`
		}
		_ = c.ShouldBindJSON(&body)
		n, err := dialer.AddDNC(c.Request.Context(), d.Pool, body.Number, body.Reason, p.Email)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"number": n})
	})

	// Find numbers through Apollo for open contacts that have none.
	s.POST("/pages/dialer/enrich", func(c *gin.Context) {
		if _, ok := personOf(c); !ok || !needPool(c, d) {
			return
		}
		var body struct {
			Limit int `json:"limit"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Limit <= 0 || body.Limit > 25 {
			body.Limit = 10
		}
		n, err := enrichPhones(c.Request.Context(), d, body.Limit)
		if err != nil && n == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"requested": n, "note": "Apollo sends the numbers back within a few minutes; they appear in the queue by themselves"})
	})
}

// enrichPhones asks Apollo about up to limit contacts with no number (each
// asked once a week at most).
func enrichPhones(ctx context.Context, d *Deps, limit int) (int, error) {
	ap := apolloFor(d)
	if !ap.Configured() {
		return 0, apollo.ErrNotConfigured
	}
	rows, err := d.Pool.Query(ctx, `SELECT c.id, COALESCE(c.person, c.name), COALESCE(c.email, ''), COALESCE(c.company, ''), COALESCE(c.linkedin, '')
		FROM founderos_funnel_contacts c LEFT JOIN founderos_dial_state s ON s.contact_id = c.id
		WHERE COALESCE(c.phone, '') = '' AND c.status IN ('first_touch', 'engaged', 'nurtured')
		  AND (s.apollo_requested_at IS NULL OR s.apollo_requested_at < now() - interval '7 days')
		ORDER BY c.likelihood DESC, c.id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type who struct{ id, name, email, company, linkedin string }
	var list []who
	for rows.Next() {
		var w who
		if rows.Scan(&w.id, &w.name, &w.email, &w.company, &w.linkedin) == nil {
			list = append(list, w)
		}
	}
	rows.Close()
	hook := hookBase() + hookApolloPath + "?k=" + hookSecret(d)
	n := 0
	var lastErr error
	for _, w := range list {
		domain := ""
		if i := strings.LastIndex(w.email, "@"); i > 0 {
			domain = w.email[i+1:]
		}
		id, phones, err := ap.Match(ctx, apollo.Person{Name: w.name, Email: w.email, Company: w.company, Domain: domain, LinkedIn: w.linkedin}, hook)
		if err != nil {
			lastErr = err
			slog.Warn("founderos dialer: apollo match failed", "contact", w.id, "err", err)
			continue
		}
		n++
		_, _ = d.Pool.Exec(ctx, `INSERT INTO founderos_dial_state (contact_id, apollo_id, apollo_requested_at) VALUES ($1, NULLIF($2, ''), now())
			ON CONFLICT (contact_id) DO UPDATE SET apollo_id = COALESCE(NULLIF($2, ''), founderos_dial_state.apollo_id), apollo_requested_at = now()`, w.id, id)
		if best := apollo.Best(phones); best != "" {
			setFoundPhone(ctx, d, w.id, best)
		}
	}
	return n, lastErr
}

func setFoundPhone(ctx context.Context, d *Deps, contactID, raw string) {
	num, err := dialer.E164(raw)
	if err != nil {
		return
	}
	_, _ = d.Pool.Exec(ctx, `UPDATE founderos_funnel_contacts SET phone = $2 WHERE id = $1 AND COALESCE(phone, '') = ''`, contactID, num)
	_, _ = d.Pool.Exec(ctx, `UPDATE founderos_dial_state SET phone_source = 'apollo', updated_at = now() WHERE contact_id = $1`, contactID)
}

// dialerBrief is the 30 seconds before the call: who, why now, an opener.
var dialerBrief = func(ctx context.Context, d *Deps, p dialer.Prospect) string {
	facts := []string{
		"Person: " + firstNonEmpty(p.Person, p.Name),
		"Company: " + p.Company,
		"Role: " + p.Role,
		"Pipeline stage: " + p.Status,
	}
	if p.LastOutcome != "" {
		facts = append(facts, "Last call: "+p.LastOutcome)
	}
	rows, err := d.Pool.Query(ctx, `SELECT label FROM founderos_funnel_touches WHERE contact_id = $1 ORDER BY seq DESC LIMIT 5`, p.ContactID)
	if err == nil {
		for rows.Next() {
			var l string
			if rows.Scan(&l) == nil {
				facts = append(facts, "History: "+l)
			}
		}
		rows.Close()
	}
	if p.Email != "" {
		var subj, body string
		if d.Pool.QueryRow(ctx, `SELECT subject, left(body, 700) FROM founderos_outbox WHERE lower(to_address) = lower($1) AND status = 'sent'
			ORDER BY sent_at DESC LIMIT 1`, p.Email).Scan(&subj, &body) == nil {
			facts = append(facts, "Our last email: "+subj+"\n"+body)
		}
	}
	plain := strings.Join(facts, "\n")
	if aiPaused() != "" {
		return plain
	}
	system := "You prepare a caller at The Founders Group (B2B sales consultancy: diagnose sales bottlenecks, build sales processes, coach the team through handover) for a cold call. " +
		"From the facts only, write three short lines: WHO (name, role, company), WHY NOW (the reason to call, from the facts), OPENER (one natural first sentence). " +
		"Plain text, no markdown, no tool calls. Never invent facts; if there is no reason to call in the facts, say so."
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := runClaude(ctx, system, plain, "haiku", 20*time.Second)
	if err != nil || strings.TrimSpace(out) == "" {
		return plain
	}
	return strings.TrimSpace(out)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// draftCallFollowUp writes the email the call promised into the Outbox
// (pending: it goes out on a click, like any first email outside a campaign).
func draftCallFollowUp(ctx context.Context, d *Deps, call dialer.Call, p people.Person) (string, error) {
	var name, company, email string
	if err := d.Pool.QueryRow(ctx, `SELECT COALESCE(person, name), COALESCE(company, ''), COALESCE(email, '')
		FROM founderos_funnel_contacts WHERE id = $1`, call.ContactID).Scan(&name, &company, &email); err != nil {
		return "", err
	}
	if email == "" {
		return "", errors.New("this contact has no email address")
	}
	notes := ""
	if call.Notes != nil {
		notes = *call.Notes
	}
	outcome := ""
	if call.Outcome != nil {
		outcome = *call.Outcome
	}
	booking := strings.TrimSpace(d.Resolver.Resolve("CALCOM_BOOKING_URL"))
	signer := firstNonEmpty(p.Name, "Nader")
	system := "You write the short follow-up email after a sales call for The Founders Group. Plain text, under 120 words, warm and specific to the notes, " +
		"no placeholders, no invented facts, no tool calls. First line is 'Subject: ...', then a blank line, then the body, signed by " + signer + ", The Founders Group."
	prompt := fmt.Sprintf("To: %s at %s\nCall outcome: %s\nCaller's notes: %s\nBooking link (include it only if a meeting is not booked yet): %s",
		name, company, outcome, notes, booking)
	out, err := runClaude(ctx, system, prompt, "sonnet", 60*time.Second)
	if err != nil {
		return "", err
	}
	subject, body := "Following up on our call", strings.TrimSpace(out)
	if first, rest, ok := strings.Cut(body, "\n"); ok && strings.HasPrefix(strings.ToLower(first), "subject:") {
		subject, body = strings.TrimSpace(first[len("subject:"):]), strings.TrimSpace(rest)
	}
	ws, err := dialerWS(ctx, d)
	if err != nil {
		return "", err
	}
	id := "dialer-" + call.ID
	_, err = d.Pool.Exec(ctx, `INSERT INTO founderos_outbox (id, workspace_id, source, source_ref, contact_id, to_address, to_name, company, subject, body)
		VALUES ($1, $2, 'dialer', $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET subject = $8, body = $9 WHERE founderos_outbox.status = 'pending'`,
		id, ws, call.ID, call.ContactID, email, name, company, subject, body)
	return id, err
}
