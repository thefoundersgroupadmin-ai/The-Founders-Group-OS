package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Prospect is what a check needs to know about one pipeline contact.
type Prospect struct {
	ID, Company, Website, Evidence, Status string
}

// Result is one check's findings.
type Result struct {
	Signals  []Signal
	Tools    []string
	Sources  []string // what was read
	Problems []string // what could not be read (the check still counts)
}

// Checker reads public pages. The zero value is not usable; use NewChecker.
type Checker struct {
	HTTP *http.Client
	// Job board API roots, swapped in tests.
	Greenhouse, Lever, Ashby, Workable string
	MaxBytes                           int64
}

// publicOnly refuses to connect to loopback, private or link-local addresses,
// so a prospect's "website" can never make the OS read its own network.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("refusing non-public address %s", host)
	}
	return nil
}

func NewChecker() *Checker {
	d := &net.Dialer{Timeout: 8 * time.Second, Control: publicOnly}
	return &Checker{
		HTTP: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 8 * time.Second, MaxIdleConnsPerHost: 2},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
		Greenhouse: "https://boards-api.greenhouse.io/v1/boards/",
		Lever:      "https://api.lever.co/v0/postings/",
		Ashby:      "https://api.ashbyhq.com/posting-api/job-board/",
		Workable:   "https://apply.workable.com/api/v1/widget/accounts/",
		MaxBytes:   1 << 20,
	}
}

const userAgent = "Mozilla/5.0 (compatible; TFG-OS signals/1.0)"

func (c *Checker) get(ctx context.Context, u string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.5")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s answered %d", hostOf(u), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxBytes))
	return body, resp.Request.URL.String(), err
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// Site normalises a website to an https URL, or "".
func Site(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	p, err := url.Parse(raw)
	if err != nil || p.Host == "" || (p.Scheme != "http" && p.Scheme != "https") {
		return ""
	}
	return p.Scheme + "://" + p.Host + "/"
}

// Check reads everything public about one prospect.
func (c *Checker) Check(ctx context.Context, p Prospect, now time.Time) Result {
	var r Result
	if s, ok := Funding(p.Evidence, now); ok {
		r.Signals = append(r.Signals, s)
		r.Sources = append(r.Sources, "research")
	}
	if p.Status == "engaged" || p.Status == "nurtured" {
		r.Signals = append(r.Signals, Signal{Kind: "engaged", Title: "Already talking to us", Detail: "replied, enquired or booked",
			Weight: 40, Fingerprint: "engaged:" + p.Status})
	}
	site := Site(p.Website)
	if site == "" {
		r.Problems = append(r.Problems, "no website on file")
		return r
	}
	home, final, err := c.get(ctx, site)
	if err != nil {
		r.Problems = append(r.Problems, "website: "+err.Error())
		return r
	}
	r.Sources = append(r.Sources, "website")
	tools, ts := Tech(string(home), final)
	r.Tools = tools
	r.Signals = append(r.Signals, ts...)

	boards := Boards(string(home))
	if len(boards) == 0 {
		base, _ := url.Parse(final)
		for i, link := range CareersLinks(string(home)) {
			if i == 2 {
				break
			}
			ref, err := url.Parse(link)
			if err != nil {
				continue
			}
			abs := base.ResolveReference(ref)
			// Only the company's own careers page, or a subdomain of it.
			if abs.Hostname() != base.Hostname() && !strings.HasSuffix(abs.Hostname(), "."+strings.TrimPrefix(base.Hostname(), "www.")) {
				continue
			}
			page, _, err := c.get(ctx, abs.String())
			if err != nil {
				continue
			}
			boards = append(boards, Boards(string(page))...)
			if len(boards) > 0 {
				break
			}
		}
	}
	for i, b := range boards {
		if i == 2 {
			break
		}
		jobs, err := c.Jobs(ctx, b)
		if err != nil {
			r.Problems = append(r.Problems, b.Provider+": "+err.Error())
			continue
		}
		r.Sources = append(r.Sources, b.Provider)
		r.Signals = append(r.Signals, Hiring(boardName(b.Provider), jobs)...)
	}
	return r
}

func boardName(p string) string {
	switch p {
	case "greenhouse":
		return "Greenhouse"
	case "lever":
		return "Lever"
	case "ashby":
		return "Ashby"
	case "workable":
		return "Workable"
	}
	return p
}

func ptime(t time.Time, err error) *time.Time {
	if err != nil || t.IsZero() {
		return nil
	}
	return &t
}

// Jobs reads a board's open roles through its public API.
func (c *Checker) Jobs(ctx context.Context, b JobBoard) ([]Job, error) {
	slug := url.PathEscape(b.Slug)
	var out []Job
	switch b.Provider {
	case "greenhouse":
		body, _, err := c.get(ctx, c.Greenhouse+slug+"/jobs")
		if err != nil {
			return nil, err
		}
		var v struct {
			Jobs []struct {
				Title       string `json:"title"`
				AbsoluteURL string `json:"absolute_url"`
				UpdatedAt   string `json:"updated_at"`
				Location    struct {
					Name string `json:"name"`
				} `json:"location"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		for _, j := range v.Jobs {
			out = append(out, Job{j.Title, j.Location.Name, j.AbsoluteURL, ptime(time.Parse(time.RFC3339, j.UpdatedAt))})
		}
	case "lever":
		body, _, err := c.get(ctx, c.Lever+slug+"?mode=json")
		if err != nil {
			return nil, err
		}
		var v []struct {
			Text       string `json:"text"`
			HostedURL  string `json:"hostedUrl"`
			CreatedAt  int64  `json:"createdAt"`
			Categories struct {
				Location string `json:"location"`
			} `json:"categories"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		for _, j := range v {
			var posted *time.Time
			if j.CreatedAt > 0 {
				t := time.UnixMilli(j.CreatedAt).UTC()
				posted = &t
			}
			out = append(out, Job{j.Text, j.Categories.Location, j.HostedURL, posted})
		}
	case "ashby":
		body, _, err := c.get(ctx, c.Ashby+slug)
		if err != nil {
			return nil, err
		}
		var v struct {
			Jobs []struct {
				Title       string `json:"title"`
				JobURL      string `json:"jobUrl"`
				Location    string `json:"location"`
				PublishedAt string `json:"publishedAt"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		for _, j := range v.Jobs {
			out = append(out, Job{j.Title, j.Location, j.JobURL, ptime(time.Parse(time.RFC3339, j.PublishedAt))})
		}
	case "workable":
		body, _, err := c.get(ctx, c.Workable+slug)
		if err != nil {
			return nil, err
		}
		var v struct {
			Jobs []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				City        string `json:"city"`
				PublishedOn string `json:"published_on"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		for _, j := range v.Jobs {
			out = append(out, Job{j.Title, j.City, j.URL, ptime(time.Parse("2006-01-02", j.PublishedOn))})
		}
	default:
		return nil, fmt.Errorf("unknown board %s", b.Provider)
	}
	return out, nil
}
