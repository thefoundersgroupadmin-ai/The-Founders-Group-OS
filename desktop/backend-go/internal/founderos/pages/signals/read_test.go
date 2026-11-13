package signals

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func TestFundingReadsRoundAmountDateAndSource(t *testing.T) {
	ev := "Company-issued announcement dated 5 Nov 2026 confirms a completed $15M Series A led by Squarepoint Capital: https://www.globenewswire.com/news-release/x.html . Profiles: https://www.linkedin.com/in/kumarnsenthil"
	s, ok := Funding(ev, now)
	if !ok {
		t.Fatal("no funding read")
	}
	if s.Title != "Raised $15M Series A round" || s.Weight != 35 || s.Date == nil || s.Date.Format("2006-01-02") != "2026-11-05" {
		t.Fatalf("got %+v", s)
	}
	if s.URL != "https://www.globenewswire.com/news-release/x.html" {
		t.Fatalf("source %q", s.URL)
	}
}

func TestFundingAgesOutAndIgnoresRevenue(t *testing.T) {
	old, ok := Funding("Raised a $4M Seed, announced 15 Sep 2024", now)
	if !ok || old.Weight != 4 {
		t.Fatalf("old round %+v", old)
	}
	if _, ok := Funding("They report $5M ARR and 40 staff.", now); ok {
		t.Fatal("revenue read as a round")
	}
	if _, ok := Funding("", now); ok {
		t.Fatal("empty evidence")
	}
	long, _ := Funding("September 3, 2026: closed a $2.5 million seed round", now)
	if long.Date == nil || long.Title != "Raised $2.5M Seed round" {
		t.Fatalf("long date %+v", long)
	}
}

func TestLinkedInPrefersBuyerLine(t *testing.T) {
	ev := "see https://www.linkedin.com/in/someone-else\nDomain: x.com; Buyer LinkedIn: https://www.linkedin.com/in/buyer; Second buyer: Y"
	if got := LinkedIn(ev); got != "https://www.linkedin.com/in/buyer" {
		t.Fatal(got)
	}
	if got := LinkedIn("profile https://www.linkedin.com/in/only."); got != "https://www.linkedin.com/in/only" {
		t.Fatal(got)
	}
}

func TestHiringRanksCommercialRoles(t *testing.T) {
	jobs := []Job{{Title: "Senior Backend Engineer"}, {Title: "Founding Account Executive", URL: "https://x/1"}, {Title: "SDR (Outbound)"},
		{Title: "Designer"}, {Title: "Customer Success Lead"}}
	got := Hiring("Ashby", jobs)
	if len(got) != 4 || got[0].Title != "Hiring: Founding Account Executive" || got[0].Weight != 30 {
		t.Fatalf("got %+v", got)
	}
	if got[1].Weight != 10 || got[3].Title != "5 open roles" {
		t.Fatalf("second and count %+v", got)
	}
	for _, title := range []string{"Software Engineer", "Founding Sales/GTM Recruiter", "Sales Engineering Intern"} {
		if l, _ := SalesRole(title); l != "" {
			t.Fatalf("%q counted as a sales hire", title)
		}
	}
	if l, _ := SalesRole("Founding Sales Manager"); l != "sales leader" {
		t.Fatal(l)
	}
	if l, _ := SalesRole("Software Engineer"); l != "" {
		t.Fatal("engineer counted as sales")
	}
}

func TestTechSpotsAMissingSalesSystem(t *testing.T) {
	tools, s := Tech(`<script src="https://example.com/app.js"></script>`, "https://a.com")
	if len(tools) != 0 || !has(s, "tech:no-sales-system") {
		t.Fatalf("%v %+v", tools, s)
	}
	// Tools behind a tag manager can't be seen: no gap is claimed.
	tools, s = Tech(`<script src="https://www.googletagmanager.com/gtm.js?id=GTM-ABC123"></script><script src="https://cdn.segment.com/x.js"></script>`, "https://a.com")
	if len(tools) != 1 || has(s, "tech:no-sales-system") || has(s, "tech:no-crm") {
		t.Fatalf("%v %+v", tools, s)
	}
	tools, s = Tech(`<script src="//js.hs-scripts.com/1.js"></script><a href="https://calendly.com/x">`, "https://b.com")
	if strings.Join(tools, ",") != "HubSpot,Calendly" || has(s, "tech:no-crm") || has(s, "tech:no-sales-system") {
		t.Fatalf("%v %+v", tools, s)
	}
}

func TestBoardsAndCareersLinks(t *testing.T) {
	html := `<a href="/careers">Careers</a><a href="https://jobs.ashbyhq.com/Decimal">x</a>
	<script src="https://boards.greenhouse.io/embed/job_board/js?for=acme"></script><a href="https://jobs.lever.co/acme/123">`
	b := Boards(html)
	if len(b) != 3 || b[0] != (JobBoard{"greenhouse", "acme"}) || b[1] != (JobBoard{"lever", "acme"}) || b[2] != (JobBoard{"ashby", "decimal"}) {
		t.Fatalf("%+v", b)
	}
	if c := CareersLinks(html); len(c) != 1 || c[0] != "/careers" {
		t.Fatalf("%v", c)
	}
}

func TestScoreAndWhyNow(t *testing.T) {
	list := []Signal{{Kind: "hiring", Title: "Hiring: SDR", Weight: 30}, {Kind: "hiring", Title: "Hiring: AE", Weight: 20},
		{Kind: "funding", Title: "Raised $4M Seed round", Weight: 35}, {Kind: "tech", Title: "Website runs X", Weight: 0}}
	if got := Score(list); got != 75 {
		t.Fatalf("score %d", got)
	}
	if got := WhyNow(list); got != "Raised $4M Seed round; Hiring: SDR" {
		t.Fatal(got)
	}
	big := []Signal{{Kind: "a", Weight: 60}, {Kind: "b", Weight: 60}}
	if Score(big) != 100 {
		t.Fatal("not capped")
	}
}

func has(list []Signal, fp string) bool {
	for _, s := range list {
		if s.Fingerprint == fp {
			return true
		}
	}
	return false
}
