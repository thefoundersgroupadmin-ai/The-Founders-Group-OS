package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

const goodBody = `Hi Dana,

I saw Reyes Co is hiring its first account executive after the seed round. Is turning founder-led deals into a repeatable process for that hire a current priority?

At The Founders Group, we help B2B teams diagnose sales bottlenecks, build sales processes and coach the team through handover.

Would a 15-minute conversation be worthwhile?

Nader
CEO, The Founders Group

If this isn't relevant, reply "no thanks" and I won't follow up.`

func TestScreenKeepsBadFirstEmailsBack(t *testing.T) {
	ok := Item{To: "dana@reyes.co", Subject: "Reyes Co's first sales hire", Body: goodBody}
	if err := Screen(ok, "thefoundersgroup.net"); err != nil {
		t.Fatalf("good email held: %v", err)
	}
	for name, it := range map[string]Item{
		"bad address":  {To: "Dana <dana@reyes.co>", Subject: ok.Subject, Body: goodBody},
		"no domain":    {To: "dana@reyes", Subject: ok.Subject, Body: goodBody},
		"our own":      {To: "nader@thefoundersgroup.net", Subject: ok.Subject, Body: goodBody},
		"no subject":   {To: ok.To, Subject: " ", Body: goodBody},
		"too short":    {To: ok.To, Subject: ok.Subject, Body: "Hi, want to talk? Nader. no thanks"},
		"placeholder":  {To: ok.To, Subject: ok.Subject, Body: strings.Replace(goodBody, "Dana", "{{first_name}}", 1)},
		"bracket name": {To: ok.To, Subject: ok.Subject, Body: strings.Replace(goodBody, "Reyes Co", "[Company]", 1)},
		"no opt-out":   {To: ok.To, Subject: ok.Subject, Body: strings.Replace(goodBody, `reply "no thanks" and I won't follow up`, "call me", 1)},
		"unsigned":     {To: ok.To, Subject: ok.Subject, Body: strings.Replace(goodBody, "Nader\n", "", 1)},
	} {
		if Screen(it, "thefoundersgroup.net") == nil {
			t.Errorf("%s: let through", name)
		}
	}
}

func campaignRow(t *testing.T, pool *pgxpool.Pool, ws, id, ref, to, status string) {
	t.Helper()
	var sentAt any
	if status == "sent" {
		sentAt = time.Now().Add(-time.Hour)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO founderos_outbox
		(id, workspace_id, source, source_ref, to_address, subject, body, status, sent_at, created_at)
		VALUES ($1, $2, 'paperclip', $3, $4, 'Reyes Co''s first sales hire', $5, $6, $7, now() + ($8 || ' seconds')::interval)`,
		id, ws, ref, to, goodBody, status, sentAt, fmt.Sprint(len(id))); err != nil {
		t.Fatal(err)
	}
}

func TestACampaignSendsOnlyItsOwnScreenedFirstEmailsOneAtATime(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	campaignRow(t, pool, ws, "a", "THE-30", "dana@reyes.co", "pending")
	campaignRow(t, pool, ws, "bb", "THE-30", "sam@reyes.co", "pending")        // same company as a
	campaignRow(t, pool, ws, "ccc", "THE-30", "ben@basalt.example", "pending") // basalt was emailed before
	campaignRow(t, pool, ws, "dddd", "THE-20", "kim@other.example", "pending") // another task: needs a click
	campaignRow(t, pool, ws, "eeeee", "THE-20", "old@basalt.example", "sent")  // the earlier send
	campaignRow(t, pool, ws, "ffffff", "THE-30", "nader@thefoundersgroup.net", "pending")

	var sent []string
	send := func(_ context.Context, it Item) error { sent = append(sent, it.To); return nil }
	c := Campaign{Ref: "THE-30", DailyCap: 20, TotalCap: 50}
	now := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := AutoSendCampaign(ctx, pool, "founderos", c, "thefoundersgroup.net", now, send); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(sent, ",") != "dana@reyes.co" {
		t.Fatalf("sent %v", sent)
	}
	for id, want := range map[string]string{"a": "sent", "bb": "pending", "ccc": "pending", "dddd": "pending", "ffffff": "pending"} {
		if got := status(t, pool, id); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
}

func TestACampaignKeepsToItsCapsAndHours(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	campaignRow(t, pool, ws, "s1", "THE-30", "a@one.example", "sent")
	campaignRow(t, pool, ws, "s2", "THE-30", "b@two.example", "sent")
	campaignRow(t, pool, ws, "p1", "THE-30", "c@three.example", "pending")
	calls := 0
	send := func(context.Context, Item) error { calls++; return nil }
	now := time.Now()
	for _, c := range []Campaign{
		{Ref: "THE-30", DailyCap: 2, TotalCap: 50},
		{Ref: "THE-30", DailyCap: 20, TotalCap: 2},
		{Ref: "THE-30", DailyCap: 20, TotalCap: 50, Window: func(time.Time) bool { return false }},
	} {
		if _, err := AutoSendCampaign(ctx, pool, "founderos", c, "", now, send); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatalf("sent past a cap or outside hours: %d", calls)
	}
	res, err := AutoSendCampaign(ctx, pool, "founderos", Campaign{Ref: "THE-30", DailyCap: 20, TotalCap: 50}, "", now,
		func(context.Context, Item) error { return errors.New("smtp: 550 mailbox unavailable") })
	if err == nil || res.Failures != 1 || status(t, pool, "p1") != "failed" {
		t.Fatalf("a failed send must be counted and recorded: %+v %v %s", res, err, status(t, pool, "p1"))
	}
}

func TestUSBusinessHours(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	for at, want := range map[time.Time]bool{
		time.Date(2026, 10, 22, 10, 0, 0, 0, ny): true,  // Thursday 10am
		time.Date(2026, 10, 22, 8, 59, 0, 0, ny): false, // too early
		time.Date(2026, 10, 22, 17, 0, 0, 0, ny): false, // done for the day
		time.Date(2026, 10, 24, 11, 0, 0, 0, ny): false, // Saturday
	} {
		if USBusinessHours(at) != want {
			t.Errorf("%v: want %v", at, want)
		}
	}
}
