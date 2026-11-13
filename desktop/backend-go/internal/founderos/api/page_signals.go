package api

// /os/signals: who to contact today and why. A background pass reads each
// open prospect's public sources about once a week (their job board, their
// website's tools, the funding the research recorded) and ranks the pipeline
// by those signals. Each row carries the next step and openers for LinkedIn,
// email and a call; a person sends every one. "Find email and phone" spends a
// Lusha credit, capped per day.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/lusha"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
	"github.com/rhl/businessos-backend/internal/founderos/pages/signals"
)

func init() { RegisterPage(registerSignals) }

const (
	signalsStale   = 7 * 24 * time.Hour
	signalsPerPass = 6
)

var (
	signalsChecker = signals.NewChecker
	lushaFor       = func(d *Deps) *lusha.Client { return lusha.New(d.Resolver) }
	signalsMu      sync.Mutex // one pass at a time, cron or button
	signalsOnce    sync.Once
	// signalsWriter writes the email draft; swapped in tests.
	signalsWriter = runClaude
)

// draftSignalsEmail writes a first email from the prospect's signals and
// files it in the Outbox, where it waits for a person to approve it. Only
// source 'paperclip' rows can ever go out by themselves, so this never does.
func draftSignalsEmail(ctx context.Context, d *Deps, wsID string, r signals.Row, signer string) (string, error) {
	if r.Email == "" {
		return "", errors.New("no email on file: find it first")
	}
	var facts []string
	for _, s := range r.Signals {
		if s.Weight > 0 {
			facts = append(facts, "- "+strings.TrimSpace(s.Title+" "+s.Detail+" "+s.URL))
		}
	}
	if len(facts) == 0 {
		return "", errors.New("no signal to write from yet: check them first")
	}
	booking := strings.TrimSpace(d.Resolver.Resolve("CALCOM_BOOKING_URL"))
	system := "You write a first cold email for The Founders Group, which builds and runs sales systems for seed to Series B founders " +
		"(offers: Sales System Build, a three-month build of the whole sales system and team training; Fractional Sales + AI SDR, a fractional sales director plus AI SDRs). " +
		"Plain text, 60 to 110 words, no placeholders, no flattery, no invented facts: use only the facts given. Open with the most specific fact. " +
		"One clear ask: a 20-minute call. First line is 'Subject: ...' (under 8 words, lower case is fine), then a blank line, then the body, signed by " + signer + ", The Founders Group."
	prompt := "To: " + firstNonEmpty(r.Person, "the founder") + ", " + firstNonEmpty(r.Role, "founder") + " at " + r.Company +
		"\nFacts (public sources):\n" + strings.Join(facts, "\n") + "\nOffer that fits: " + firstNonEmpty(r.Plays.Offer, "Sales System Build") +
		"\nOpener idea: " + r.Plays.Email + "\nBooking link (end with it if given): " + booking
	subject, body := "quick idea for "+r.Company, ""
	out, err := signalsWriter(ctx, system, prompt, "sonnet", 60*time.Second)
	if err == nil && strings.TrimSpace(out) != "" {
		body = strings.TrimSpace(out)
		if first, rest, ok := strings.Cut(body, "\n"); ok && strings.HasPrefix(strings.ToLower(first), "subject:") {
			subject, body = strings.TrimSpace(first[len("subject:"):]), strings.TrimSpace(rest)
		}
	} else {
		// No writer available: the drafted opener plus a plain ask, still for review.
		body = r.Plays.Email + "\n\nWorth a 20-minute call to see if it fits?"
		if booking != "" {
			body += " " + booking
		}
		body += "\n\n" + signer + "\nThe Founders Group"
	}
	id := "signals-" + r.ContactID
	_, err = d.Pool.Exec(ctx, `INSERT INTO founderos_outbox (id, workspace_id, source, source_ref, contact_id, to_address, to_name, company, subject, body)
		VALUES ($1, $2::uuid, 'signals', $3, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET subject = $7, body = $8 WHERE founderos_outbox.status = 'pending'`,
		id, wsID, r.ContactID, r.Email, r.Person, r.Company, subject, body)
	return id, err
}

func lushaDailyCap() int { return envInt("FOUNDEROS_LUSHA_DAILY_CAP", 20) }

// checkSignals reads up to n due prospects (or the one named) and saves what it found.
func checkSignals(ctx context.Context, d *Deps, wsID, contactID string, n int) (checked int, err error) {
	if !signalsMu.TryLock() {
		return 0, errors.New("a signal check is already running")
	}
	defer signalsMu.Unlock()
	var list []signals.Prospect
	if contactID != "" {
		p, err := signals.One(ctx, d.Pool, wsID, contactID)
		if err != nil {
			return 0, err
		}
		list = []signals.Prospect{p}
	} else if list, err = signals.Due(ctx, d.Pool, wsID, signalsStale, n); err != nil {
		return 0, err
	}
	ch := signalsChecker()
	type out struct {
		p signals.Prospect
		r signals.Result
	}
	results := make(chan out, len(list))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, p := range list {
		wg.Add(1)
		go func(p signals.Prospect) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results <- out{p, ch.Check(ctx, p, time.Now())}
		}(p)
	}
	wg.Wait()
	close(results)
	for o := range results {
		if err := signals.Save(ctx, d.Pool, wsID, o.p, o.r); err != nil {
			return checked, err
		}
		checked++
	}
	return checked, nil
}

func startSignals(d *Deps) {
	if d.Pool == nil || testing.Testing() {
		return
	}
	signalsOnce.Do(func() {
		go func() {
			t := time.NewTicker(10 * time.Minute)
			defer t.Stop()
			for range t.C {
				guard.RunCron("signals", func() error {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					ws, err := dialerWS(ctx, d)
					if err != nil {
						return err
					}
					n, err := checkSignals(ctx, d, ws, "", signalsPerPass)
					if n > 0 {
						slog.Info("founderos signals: checked prospects", "n", n)
					}
					return err
				})
			}
		}()
	})
}

func lushaUsedToday(ctx context.Context, d *Deps) int {
	var n int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM founderos_enrichments WHERE provider = 'lusha' AND at >= date_trunc('day', now() AT TIME ZONE 'Europe/London') AT TIME ZONE 'Europe/London'`).Scan(&n)
	return n
}

func domainOf(site string) string {
	s := signals.Site(site)
	if s == "" {
		return ""
	}
	u, _ := url.Parse(s)
	return strings.TrimPrefix(u.Hostname(), "www.")
}

func registerSignals(s *gin.RouterGroup, d *Deps) {
	startSignals(d)

	s.GET("/pages/signals", func(c *gin.Context) {
		if _, ok := personOf(c); !ok || !needPool(c, d) {
			return
		}
		ws, ok := wsOf(c, d, outboxWorkspace)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		rows, st, err := signals.Board(ctx, d.Pool, ws, 200)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		lc := lushaFor(d)
		c.JSON(http.StatusOK, gin.H{
			"rows":           rows,
			"stats":          st,
			"lusha":          gin.H{"configured": lc.Configured(), "usedToday": lushaUsedToday(ctx, d), "dailyCap": lushaDailyCap()},
			"outreachPaused": campaignHalted() != "",
			"autoChecks":     guard.CronsEnabled(),
		})
	})

	// Check now: one prospect, or the next few due.
	s.POST("/pages/signals/check", func(c *gin.Context) {
		if _, ok := personOf(c); !ok || !needPool(c, d) {
			return
		}
		ws, ok := wsOf(c, d, outboxWorkspace)
		if !ok {
			return
		}
		var body struct {
			ContactID string `json:"contactId"`
		}
		_ = c.ShouldBindJSON(&body)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
		defer cancel()
		n, err := checkSignals(ctx, d, ws, strings.TrimSpace(body.ContactID), signalsPerPass)
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such prospect"})
			return
		}
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "checked": n})
			return
		}
		c.JSON(http.StatusOK, gin.H{"checked": n})
	})

	// Find email and phone through Lusha (one credit), filling only empty fields.
	s.POST("/pages/signals/enrich", func(c *gin.Context) {
		if _, ok := personOf(c); !ok || !needPool(c, d) {
			return
		}
		ws, ok := wsOf(c, d, outboxWorkspace)
		if !ok {
			return
		}
		var body struct {
			ContactID string `json:"contactId"`
		}
		if c.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.ContactID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contactId required"})
			return
		}
		ctx := c.Request.Context()
		lc := lushaFor(d)
		if !lc.Configured() {
			c.JSON(http.StatusOK, gin.H{"state": "not_configured", "error": lusha.ErrNotConfigured.Error()})
			return
		}
		if used := lushaUsedToday(ctx, d); used >= lushaDailyCap() {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "today's Lusha lookups are used up (FOUNDEROS_LUSHA_DAILY_CAP)"})
			return
		}
		var person, website, linkedin, email, phone string
		err := d.Pool.QueryRow(ctx, `SELECT COALESCE(person, name), COALESCE(website, ''), COALESCE(linkedin, ''), COALESCE(email, ''), COALESCE(phone, '')
			FROM founderos_funnel_contacts WHERE workspace_id = $1::uuid AND id = $2`, ws, body.ContactID).Scan(&person, &website, &linkedin, &email, &phone)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such prospect"})
			return
		}
		f, ferr := lc.Find(ctx, lusha.Person{Ref: body.ContactID, Name: person, Domain: domainOf(website), LinkedIn: linkedin})
		problem := ""
		if ferr != nil {
			problem = ferr.Error()
		}
		if !errors.Is(ferr, guard.ErrWritesDisabled) {
			_, _ = d.Pool.Exec(ctx, `INSERT INTO founderos_enrichments (workspace_id, contact_id, provider, found_email, found_phone, problem)
				VALUES ($1::uuid, $2, 'lusha', $3, $4, $5)`, ws, body.ContactID, f.Email, f.Phone, problem)
		}
		if ferr != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": problem})
			return
		}
		if _, err := d.Pool.Exec(ctx, `UPDATE founderos_funnel_contacts SET
				email = CASE WHEN COALESCE(email, '') = '' AND $3 <> '' THEN $3 ELSE email END,
				phone = CASE WHEN COALESCE(phone, '') = '' AND $4 <> '' THEN $4 ELSE phone END
			WHERE workspace_id = $1::uuid AND id = $2`, ws, body.ContactID, f.Email, f.Phone); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"email": f.Email, "phone": f.Phone, "found": f.Email != "" || f.Phone != ""})
	})

	// Draft the first email into the Outbox (waits for approval there).
	s.POST("/pages/signals/draft", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok || !needPool(c, d) {
			return
		}
		ws, ok := wsOf(c, d, outboxWorkspace)
		if !ok {
			return
		}
		var body struct {
			ContactID string `json:"contactId"`
		}
		if c.ShouldBindJSON(&body) != nil || body.ContactID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contactId required"})
			return
		}
		ctx := c.Request.Context()
		rows, _, err := signals.Board(ctx, d.Pool, ws, 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, r := range rows {
			if r.ContactID != body.ContactID {
				continue
			}
			id, err := draftSignalsEmail(ctx, d, ws, r, firstNonEmpty(p.Name, "Nader"))
			if err != nil {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"id": id})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "no such open prospect"})
	})

	// The rep sent the LinkedIn note: note it on the prospect and come back in three days.
	s.POST("/pages/signals/touched", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok || !needPool(c, d) {
			return
		}
		ws, ok := wsOf(c, d, outboxWorkspace)
		if !ok {
			return
		}
		var body struct {
			ContactID string `json:"contactId"`
			Channel   string `json:"channel"`
		}
		if c.ShouldBindJSON(&body) != nil || body.ContactID == "" || (body.Channel != "linkedin" && body.Channel != "email") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contactId and channel (linkedin or email) required"})
			return
		}
		what := map[string]string{"linkedin": "LinkedIn connection sent", "email": "Email sent by hand"}[body.Channel]
		tag, err := d.Pool.Exec(c.Request.Context(), `UPDATE founderos_funnel_contacts SET
				next_action = $3, next_action_at = now() + interval '3 days'
			WHERE workspace_id = $1::uuid AND id = $2`, ws, body.ContactID,
			what+" "+time.Now().In(statsZone()).Format("2 Jan")+" by "+p.Email+"; follow up if no reply")
		if err != nil || tag.RowsAffected() == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such prospect"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
