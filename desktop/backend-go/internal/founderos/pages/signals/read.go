// Package signals finds why a prospect is worth contacting now, from public
// sources only: open roles on the company's own job board (Greenhouse, Lever,
// Ashby, Workable), the tools its website runs, the funding the research
// already recorded, and whether they have engaged. Each signal keeps where it
// came from. Nothing here sends anything.
package signals

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Signal is one observed fact about a prospect.
type Signal struct {
	Kind   string     `json:"kind"` // hiring | funding | tech | engaged
	Title  string     `json:"title"`
	Detail string     `json:"detail,omitempty"`
	URL    string     `json:"url,omitempty"`
	Weight int        `json:"weight"`
	Date   *time.Time `json:"date,omitempty"`
	// Fingerprint makes the same fact land on the same row every pass.
	Fingerprint string `json:"-"`
}

// ---- funding, read from the research the agents already wrote ---------------

var (
	moneyRe = regexp.MustCompile(`(?i)\$\s?(\d+(?:\.\d+)?)\s?(m|mm|million|b|billion|k)\b`)
	roundRe = regexp.MustCompile(`(?i)\b(pre-seed|seed|series [a-e])\b`)
	dateRes = []struct {
		re     *regexp.Regexp
		layout string
	}{
		{regexp.MustCompile(`\b(\d{1,2} (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Sept|Oct|Nov|Dec)[a-z]* \d{4})\b`), "2 Jan 2006"},
		{regexp.MustCompile(`\b((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Sept|Oct|Nov|Dec)[a-z]* \d{1,2},? \d{4})\b`), "Jan 2 2006"},
		{regexp.MustCompile(`\b(20\d{2}-\d{2}-\d{2})\b`), "2006-01-02"},
	}
	urlRe = regexp.MustCompile(`https?://[^\s,;)\]]+`)
)

func parseDate(s string) *time.Time {
	for _, d := range dateRes {
		m := d.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		v := strings.ReplaceAll(m[1], ",", "")
		// Long month names ("September") → three letters for the layout.
		parts := strings.Fields(v)
		for i, p := range parts {
			if len(p) > 3 && p[0] >= 'A' && p[0] <= 'Z' {
				parts[i] = p[:3]
			}
		}
		if t, err := time.Parse(d.layout, strings.Join(parts, " ")); err == nil {
			return &t
		}
	}
	return nil
}

// Funding reads the first round the research names ("$4M Seed", "Series A"),
// with its announcement date and source link when written down.
func Funding(evidence string, now time.Time) (Signal, bool) {
	ev := strings.TrimSpace(evidence)
	if ev == "" {
		return Signal{}, false
	}
	round := roundRe.FindString(ev)
	money := moneyRe.FindStringSubmatch(ev)
	if round == "" && money == nil {
		return Signal{}, false
	}
	// Only a sentence that talks about raising counts; "$5M ARR" is not a round.
	if round == "" && !regexp.MustCompile(`(?i)rais|round|funding|led by|investors?`).MatchString(ev) {
		return Signal{}, false
	}
	amount := ""
	if money != nil {
		unit := strings.ToUpper(money[2][:1])
		if unit == "K" {
			amount = "$" + money[1] + "K"
		} else {
			amount = "$" + money[1] + unit
		}
	}
	title := strings.TrimSpace(strings.Join(nonEmpty(amount, titleCase(round)), " ")) + " round"
	s := Signal{Kind: "funding", Title: "Raised " + title, Fingerprint: "funding:" + strings.ToLower(title)}
	s.Date = parseDate(ev)
	for _, u := range urlRe.FindAllString(ev, -1) {
		u = strings.TrimRight(u, ".")
		if !strings.Contains(u, "linkedin.com/in/") {
			s.URL = u
			break
		}
	}
	s.Weight = 15
	if s.Date != nil {
		age := now.Sub(*s.Date)
		switch {
		case age < 0:
			s.Weight = 30
		case age <= 45*24*time.Hour:
			s.Weight = 35
		case age <= 120*24*time.Hour:
			s.Weight = 25
		case age <= 365*24*time.Hour:
			s.Weight = 12
		default:
			s.Weight = 4
		}
		s.Detail = "announced " + s.Date.Format("2 Jan 2006")
	}
	return s, true
}

func nonEmpty(v ...string) []string {
	var out []string
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func titleCase(s string) string {
	if s == "" {
		return ""
	}
	l := strings.ToLower(s)
	switch {
	case l == "seed":
		return "Seed"
	case l == "pre-seed":
		return "Pre-Seed"
	case strings.HasPrefix(l, "series "):
		return "Series " + strings.ToUpper(l[7:])
	}
	return s
}

// LinkedIn pulls the buyer's profile out of the research ("Buyer LinkedIn: …"),
// falling back to the first personal profile linked.
func LinkedIn(evidence string) string {
	if m := regexp.MustCompile(`(?i)Buyer LinkedIn:\s*(https?://[^\s;,]+)`).FindStringSubmatch(evidence); m != nil {
		return strings.TrimRight(m[1], ".")
	}
	for _, u := range urlRe.FindAllString(evidence, -1) {
		if strings.Contains(u, "linkedin.com/in/") {
			return strings.TrimRight(u, ".")
		}
	}
	return ""
}

// ---- hiring ------------------------------------------------------------------

// Job is one open role on a public board.
type Job struct {
	Title, Location, URL string
	Posted               *time.Time
}

var roleRules = []struct {
	re     *regexp.Regexp
	label  string
	weight int
}{
	{regexp.MustCompile(`(?i)\b(sdr|bdr|sales development|business development rep|outbound)\b`), "SDR/BDR", 30},
	{regexp.MustCompile(`(?i)\b(head of sales|vp,? (of )?sales|sales director|director of sales|chief revenue|cro|founding (ae|account executive|sales)|first sales)\b`), "sales leader", 30},
	{regexp.MustCompile(`(?i)\b(account executive|\bae\b|sales manager|sales lead|enterprise sales|sales engineer|solutions engineer|account manager)\b`), "seller", 22},
	{regexp.MustCompile(`(?i)\b(revops|revenue operations|sales operations|sales ops|gtm|go-to-market)\b`), "revenue ops", 20},
	{regexp.MustCompile(`(?i)\b(growth|demand gen|marketing|partnerships|customer success)\b`), "go-to-market", 6},
}

// notSelling are roles that mention sales without selling (they recruit,
// design or build for the sales team).
var notSelling = regexp.MustCompile(`(?i)\b(recruit\w*|talent|people partner|engineer(ing)? manager|software|developer|designer|data scientist|intern)\b`)

// SalesRole says what kind of commercial hire a job title is, if any.
func SalesRole(title string) (label string, weight int) {
	if notSelling.MatchString(title) {
		return "", 0
	}
	for _, r := range roleRules {
		if r.re.MatchString(title) {
			return r.label, r.weight
		}
	}
	return "", 0
}

// Hiring turns a board's open roles into signals: each commercial role is one
// signal (strongest first, at most three), and a busy board is one more.
func Hiring(board string, jobs []Job) []Signal {
	type hit struct {
		job    Job
		label  string
		weight int
	}
	var hits []hit
	for _, j := range jobs {
		if l, w := SalesRole(j.Title); w > 0 {
			hits = append(hits, hit{j, l, w})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].weight > hits[b].weight })
	var out []Signal
	for i, h := range hits {
		if i == 3 {
			break
		}
		s := Signal{Kind: "hiring", Title: "Hiring: " + h.job.Title, Detail: h.label + " role on " + board,
			URL: h.job.URL, Weight: h.weight, Date: h.job.Posted,
			Fingerprint: "hiring:" + strings.ToLower(board+":"+h.job.Title)}
		if h.job.Location != "" {
			s.Detail += " · " + h.job.Location
		}
		if i > 0 { // the second and third role add less than the first
			s.Weight = h.weight / 3
		}
		out = append(out, s)
	}
	if len(jobs) >= 5 {
		out = append(out, Signal{Kind: "hiring", Title: strconv.Itoa(len(jobs)) + " open roles", Detail: "growing team on " + board,
			Weight: 5, Fingerprint: "hiring:" + strings.ToLower(board) + ":count"})
	}
	return out
}

// ---- tech stack ------------------------------------------------------------

type fingerprint struct {
	name, group string
	re          *regexp.Regexp
}

var techPrints = []fingerprint{
	{"HubSpot", "crm", regexp.MustCompile(`(?i)js\.hs-scripts\.com|js\.hsforms\.net|hs-analytics\.net|js\.hubspot\.com|hbspt\.forms`)},
	{"Salesforce", "crm", regexp.MustCompile(`(?i)pardot\.com|pi\.pardot|force\.com/|salesforce-sites|webto(lead|case)`)},
	{"Marketo", "crm", regexp.MustCompile(`(?i)munchkin\.marketo|marketo\.com/js`)},
	{"Pipedrive", "crm", regexp.MustCompile(`(?i)leadbooster|pipedriveassets|pipedrive\.com/`)},
	{"Intercom", "chat", regexp.MustCompile(`(?i)widget\.intercom\.io|intercomcdn|intercomSettings`)},
	{"Drift", "chat", regexp.MustCompile(`(?i)js\.driftt\.com|drift\.com/`)},
	{"Qualified", "chat", regexp.MustCompile(`(?i)js\.qualified\.com`)},
	{"Crisp", "chat", regexp.MustCompile(`(?i)client\.crisp\.chat`)},
	{"Zendesk", "chat", regexp.MustCompile(`(?i)static\.zdassets\.com|zopim`)},
	{"Calendly", "booking", regexp.MustCompile(`(?i)calendly\.com/`)},
	{"Chili Piper", "booking", regexp.MustCompile(`(?i)chilipiper\.com`)},
	{"Cal.com", "booking", regexp.MustCompile(`(?i)cal\.com/[a-z0-9]|app\.cal\.com`)},
	{"Clearbit", "intent", regexp.MustCompile(`(?i)clearbit\.com|tag\.clearbitscripts`)},
	{"6sense", "intent", regexp.MustCompile(`(?i)6sc\.co|6sense\.com`)},
	{"RB2B", "intent", regexp.MustCompile(`(?i)rb2b\.com|reb2b`)},
	{"Warmly", "intent", regexp.MustCompile(`(?i)warmly\.ai|opps-widget\.getwarmly`)},
	{"Apollo", "intent", regexp.MustCompile(`(?i)assets\.apollo\.io|apollo\.io/js`)},
	{"Segment", "data", regexp.MustCompile(`(?i)cdn\.segment\.com|analytics\.js`)},
	{"Stripe", "data", regexp.MustCompile(`(?i)js\.stripe\.com`)},
}

var tagManager = regexp.MustCompile(`(?i)googletagmanager\.com/gtm\.js|GTM-[A-Z0-9]{4,}|segment\.com/analytics\.js|cdn\.segment\.com|tealium|rudderstack`)

// Tech lists the sales and marketing tools a homepage loads, and the one
// signal that matters to us: whether it has a sales system at all.
func Tech(html string, pageURL string) (tools []string, s []Signal) {
	groups := map[string]bool{}
	for _, f := range techPrints {
		if f.re.MatchString(html) {
			tools = append(tools, f.name)
			groups[f.group] = true
		}
	}
	if len(tools) > 0 {
		s = append(s, Signal{Kind: "tech", Title: "Website runs " + strings.Join(tools, ", "), URL: pageURL, Weight: 0,
			Fingerprint: "tech:stack"})
	}
	// A tag manager loads tools the page source doesn't show, so a gap can't be told.
	hidden := tagManager.MatchString(html)
	if hidden {
		// say nothing about what's missing
	} else if !groups["crm"] && !groups["chat"] && !groups["booking"] {
		s = append(s, Signal{Kind: "tech", Title: "No CRM, chat or booking on the website",
			Detail: "inbound goes nowhere tracked: a sales system gap", URL: pageURL, Weight: 8, Fingerprint: "tech:no-sales-system"})
	} else if !groups["crm"] {
		s = append(s, Signal{Kind: "tech", Title: "No CRM tracking on the website", URL: pageURL, Weight: 5, Fingerprint: "tech:no-crm"})
	}
	if groups["intent"] {
		s = append(s, Signal{Kind: "tech", Title: "Already buys outbound data tools", Detail: "they invest in outbound", URL: pageURL, Weight: 6,
			Fingerprint: "tech:outbound-tools"})
	}
	return tools, s
}

// ---- job boards linked from the website --------------------------------------

// JobBoard is a job board the company links to.
type JobBoard struct {
	Provider, Slug string
}

var boardRes = []struct {
	provider string
	re       *regexp.Regexp
}{
	{"greenhouse", regexp.MustCompile(`(?i)(?:boards|job-boards)(?:\.eu)?\.greenhouse\.io/(?:embed/job_board(?:/js)?\?for=)?([a-z0-9_-]+)`)},
	{"lever", regexp.MustCompile(`(?i)jobs\.lever\.co/([a-z0-9_.-]+)`)},
	{"ashby", regexp.MustCompile(`(?i)jobs\.ashbyhq\.com/([a-z0-9_.%-]+)`)},
	{"workable", regexp.MustCompile(`(?i)apply\.workable\.com/([a-z0-9_-]+)`)},
}

var boardSkip = map[string]bool{"embed": true, "api": true, "js": true, "v1": true, "jobs": true, "j": true, "": true}

// Boards finds the job boards a page links to, each once.
func Boards(html string) []JobBoard {
	seen := map[string]bool{}
	var out []JobBoard
	for _, b := range boardRes {
		for _, m := range b.re.FindAllStringSubmatch(html, -1) {
			slug := strings.ToLower(strings.TrimRight(m[1], "."))
			if boardSkip[slug] || seen[b.provider+slug] {
				continue
			}
			seen[b.provider+slug] = true
			out = append(out, JobBoard{b.provider, slug})
		}
	}
	return out
}

var atsHost = regexp.MustCompile(`(?i)greenhouse\.io|lever\.co|ashbyhq\.com|workable\.com`)

var careersRe = regexp.MustCompile(`(?i)href=["']([^"'#]*(?:careers|jobs|join-us|join|work-with-us|hiring)[^"'#]*)["']`)

// CareersLinks are the site's own links that look like a careers page.
func CareersLinks(html string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range careersRe.FindAllStringSubmatch(html, -1) {
		if atsHost.MatchString(m[1]) {
			continue // a job board link is read as a board, not a page
		}
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ---- score ---------------------------------------------------------------------

// Score adds the signals, the strongest of each kind counting in full and the
// rest at half, capped at 100.
func Score(list []Signal) int {
	byKind := map[string][]int{}
	for _, s := range list {
		if s.Weight > 0 {
			byKind[s.Kind] = append(byKind[s.Kind], s.Weight)
		}
	}
	total := 0
	for _, ws := range byKind {
		sort.Sort(sort.Reverse(sort.IntSlice(ws)))
		for i, w := range ws {
			if i == 0 {
				total += w
			} else {
				total += w / 2
			}
		}
	}
	if total > 100 {
		total = 100
	}
	return total
}

// WhyNow is the one line a rep reads before reaching out.
func WhyNow(list []Signal) string {
	sorted := append([]Signal(nil), list...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Weight > sorted[b].Weight })
	var parts []string
	kinds := map[string]bool{}
	for _, s := range sorted {
		if s.Weight <= 0 || kinds[s.Kind] {
			continue
		}
		kinds[s.Kind] = true
		parts = append(parts, s.Title)
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, "; ")
}
