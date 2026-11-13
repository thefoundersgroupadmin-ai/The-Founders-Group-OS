package signals

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testChecker(t *testing.T, site http.HandlerFunc, ats http.HandlerFunc) (*Checker, string) {
	t.Helper()
	s := httptest.NewServer(site)
	a := httptest.NewServer(ats)
	t.Cleanup(s.Close)
	t.Cleanup(a.Close)
	c := NewChecker()
	c.HTTP = s.Client() // tests talk to 127.0.0.1, which the real client refuses
	c.Greenhouse, c.Lever, c.Ashby, c.Workable = a.URL+"/gh/", a.URL+"/lever/", a.URL+"/ashby/", a.URL+"/wk/"
	return c, s.URL
}

func TestCheckFollowsCareersPageToTheBoard(t *testing.T) {
	c, site := testChecker(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<html><a href="/careers">Join us</a><script src="/app.js"></script></html>`))
		case "/careers":
			_, _ = w.Write([]byte(`<a href="https://jobs.ashbyhq.com/acme">Open roles</a>`))
		default:
			http.NotFound(w, r)
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ashby/acme" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"jobs":[{"title":"Founding Account Executive","jobUrl":"https://jobs.ashbyhq.com/acme/1","location":"NYC","publishedAt":"2026-11-01T00:00:00Z"},{"title":"Engineer"}]}`))
	})
	r := c.Check(context.Background(), Prospect{ID: "p1", Website: site, Evidence: "Raised a $4M Seed round, announced 1 Nov 2026", Status: "first_touch"}, now)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", r.Problems)
	}
	if !has(r.Signals, "hiring:ashby:founding account executive") || !has(r.Signals, "funding:$4m seed round") || !has(r.Signals, "tech:no-sales-system") {
		t.Fatalf("signals %+v", r.Signals)
	}
	if strings.Join(r.Sources, ",") != "research,website,ashby" {
		t.Fatalf("sources %v", r.Sources)
	}
	if Score(r.Signals) != 73 {
		t.Fatalf("score %d", Score(r.Signals))
	}
}

func TestCheckReadsEachBoardAPI(t *testing.T) {
	c, _ := testChecker(t, http.NotFound, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gh/acme/jobs":
			_, _ = w.Write([]byte(`{"jobs":[{"title":"SDR","absolute_url":"u","location":{"name":"Remote"},"updated_at":"2026-11-02T10:00:00-05:00"}]}`))
		case "/lever/acme":
			_, _ = w.Write([]byte(`[{"text":"Head of Sales","hostedUrl":"u","createdAt":1762000000000,"categories":{"location":"SF"}}]`))
		case "/wk/acme":
			_, _ = w.Write([]byte(`{"jobs":[{"title":"RevOps Manager","url":"u","city":"Austin","published_on":"2026-10-30"}]}`))
		default:
			http.NotFound(w, r)
		}
	})
	for p, want := range map[string]string{"greenhouse": "SDR", "lever": "Head of Sales", "workable": "RevOps Manager"} {
		jobs, err := c.Jobs(context.Background(), JobBoard{p, "acme"})
		if err != nil || len(jobs) != 1 || jobs[0].Title != want || jobs[0].Posted == nil {
			t.Fatalf("%s: %+v %v", p, jobs, err)
		}
	}
}

func TestCheckWithoutWebsiteStillUsesResearch(t *testing.T) {
	c := NewChecker()
	r := c.Check(context.Background(), Prospect{Evidence: "Series A led by X", Status: "engaged"}, now)
	if len(r.Signals) != 2 || r.Problems[0] != "no website on file" {
		t.Fatalf("%+v", r)
	}
}

func TestRealClientRefusesTheOSOwnNetwork(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer s.Close()
	r := NewChecker().Check(context.Background(), Prospect{Website: s.URL}, now)
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "refusing non-public address") {
		t.Fatalf("%+v", r)
	}
}

func TestSite(t *testing.T) {
	for in, want := range map[string]string{"decimal.app": "https://decimal.app/", "https://www.firmly.ai/about": "https://www.firmly.ai/", "ftp://x": "", "": ""} {
		if got := Site(in); got != want {
			t.Fatalf("%q → %q", in, got)
		}
	}
}
