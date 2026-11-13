package signals

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func TestSaveBoardAndDue(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	for _, c := range []struct{ id, person, company, status, email, ev string }{
		{"c1", "Sam Lee", "Acme", "first_touch", "", "Buyer LinkedIn: https://www.linkedin.com/in/samlee; raised a $4M Seed"},
		{"c2", "Ana Ruiz", "Beta", "engaged", "ana@beta.example", ""},
		{"c3", "Old Co", "Gamma", "converted", "", ""},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, person, company, email, research_evidence, website, created_at)
			VALUES ($1, $2::uuid, $3, 'vantage', $4, $3, $5, NULLIF($6, ''), $7, 'acme.example', now())`, c.id, ws, c.person, c.status, c.company, c.email, c.ev); err != nil {
			t.Fatal(err)
		}
	}
	due, err := Due(ctx, pool, ws, 7*24*time.Hour, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due %v %v", due, err)
	}
	p, _ := One(ctx, pool, ws, "c1")
	hire := Signal{Kind: "hiring", Title: "Hiring: SDR", Detail: "SDR/BDR role on Ashby", Weight: 30, Fingerprint: "hiring:a"}
	fund := Signal{Kind: "funding", Title: "Raised $4M Seed round", Weight: 25, Fingerprint: "funding:x"}
	if err := Save(ctx, pool, ws, p, Result{Signals: []Signal{hire, fund}, Sources: []string{"website"}}); err != nil {
		t.Fatal(err)
	}
	// The role closes: the next check drops it.
	if err := Save(ctx, pool, ws, p, Result{Signals: []Signal{fund}, Sources: []string{"website"}, Problems: []string{"ashby: down"}}); err != nil {
		t.Fatal(err)
	}
	rows, st, err := Board(ctx, pool, ws, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || st.Open != 2 || st.Checked != 1 || st.Funded != 1 || st.Hiring != 0 {
		t.Fatalf("rows %d stats %+v", len(rows), st)
	}
	// Engaged (40 from nothing stored → 0) vs funded 25: the funded one leads.
	top := rows[0]
	if top.ContactID != "c1" || top.Score != 25 || len(top.Signals) != 1 || top.LinkedIn != "https://www.linkedin.com/in/samlee" {
		t.Fatalf("top %+v", top)
	}
	if top.Plays.Next != "linkedin" || !strings.HasPrefix(top.Plays.LinkedIn, "Hi Sam, congrats on the $4M Seed") || top.Problem != "ashby: down" {
		t.Fatalf("plays %+v", top.Plays)
	}
	if rows[1].Plays.Next != "reply" {
		t.Fatalf("engaged next %+v", rows[1].Plays)
	}
	due, _ = Due(ctx, pool, ws, 7*24*time.Hour, 10)
	if len(due) != 1 || due[0].ID != "c2" {
		t.Fatalf("due after check %+v", due)
	}
}

func TestPlaysPickOfferAndChannel(t *testing.T) {
	r := Row{Person: "Kim Park", Company: "Delta", Phone: "+12125550000",
		Signals: []Signal{{Kind: "hiring", Title: "Hiring: SDR", Detail: "SDR/BDR role on Lever", Weight: 30}}}
	p := PlaysFor(r)
	if p.Next != "call" || p.Offer != "Fractional Sales + AI SDR" || !strings.Contains(p.Call, "Delta is hiring a SDR") {
		t.Fatalf("%+v", p)
	}
	if len(PlaysFor(Row{Person: "A", Company: "B", Signals: []Signal{{Kind: "hiring", Title: "Hiring: " + strings.Repeat("x", 400), Weight: 22}}}).LinkedIn) > 300 {
		t.Fatal("LinkedIn note over 300 characters")
	}
	if PlaysFor(Row{}).Next != "research" || PlaysFor(Row{Person: "X"}).Next != "find_contact" {
		t.Fatal("fallbacks")
	}
}
