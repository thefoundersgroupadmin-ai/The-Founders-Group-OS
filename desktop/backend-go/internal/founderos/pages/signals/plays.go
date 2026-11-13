package signals

import (
	"strings"
)

// Plays is what to do next with a prospect and the words to start with, all
// built from the facts on file. A person sends every one of them.
type Plays struct {
	Next      string `json:"next"`      // reply | call | email | linkedin | find_contact | research
	NextLabel string `json:"nextLabel"` // the button text
	LinkedIn  string `json:"linkedinNote,omitempty"`
	Email     string `json:"emailOpener,omitempty"`
	Call      string `json:"callOpener,omitempty"`
	Offer     string `json:"offer,omitempty"` // which TFG offer fits
}

func firstName(person string) string {
	f := strings.Fields(person)
	if len(f) == 0 {
		return "there"
	}
	return f[0]
}

func strongest(list []Signal, kind string) *Signal {
	var best *Signal
	for i := range list {
		if list[i].Kind == kind && list[i].Weight > 0 && (best == nil || list[i].Weight > best.Weight) {
			best = &list[i]
		}
	}
	return best
}

// hook is the one specific reason, in words a founder would say.
func hook(r Row) (line, offer string) {
	company := r.Company
	if company == "" {
		company = "the team"
	}
	hire := strongest(r.Signals, "hiring")
	fund := strongest(r.Signals, "funding")
	gap := false
	for _, s := range r.Signals {
		if s.Kind == "tech" && (strings.HasPrefix(s.Fingerprint, "tech:no-") || strings.HasPrefix(s.Title, "No ")) {
			gap = true
		}
	}
	role := ""
	if hire != nil {
		role = strings.TrimPrefix(hire.Title, "Hiring: ")
	}
	switch {
	case hire != nil && fund != nil:
		return "Congrats on the " + strings.TrimSuffix(strings.TrimPrefix(fund.Title, "Raised "), " round") + ", and I saw " + company +
			" is hiring a " + role + ". New sellers ramp far faster when the pipeline and process are built before day one.", "Sales System Build"
	case hire != nil && strings.Contains(strings.ToLower(hire.Detail), "sdr"):
		return "I saw " + company + " is hiring a " + role + ". We run AI SDRs alongside a fractional sales lead, so outbound starts this month rather than after a hire ramps.", "Fractional Sales + AI SDR"
	case hire != nil:
		return "I saw " + company + " is hiring a " + role + ". The first sales hires do best when the system they walk into is already built.", "Sales System Build"
	case fund != nil:
		return "Congrats on the " + strings.TrimSuffix(strings.TrimPrefix(fund.Title, "Raised "), " round") + ". Most teams at this stage are still selling founder-led with no repeatable system behind it.", "Sales System Build"
	case gap:
		return "I noticed " + company + "'s site has no CRM or booking behind it, so inbound interest is hard to track.", "Sales System Build"
	}
	return "", ""
}

// PlaysFor picks the next step (warmest channel we can actually use) and
// drafts the openers.
func PlaysFor(r Row) Plays {
	var p Plays
	line, offer := hook(r)
	p.Offer = offer
	name := firstName(r.Person)
	if line != "" {
		note := "Hi " + name + ", " + lowerFirst(line) + " I help seed to Series B founders build their sales engine. Happy to connect."
		if len(note) > 300 { // LinkedIn's connection-note limit
			note = "Hi " + name + ", " + lowerFirst(line)
			if len(note) > 300 {
				note = note[:297] + "..."
			}
		}
		p.LinkedIn = note
		p.Email = "Hi " + name + ",\n\n" + line
		p.Call = "Hi " + name + ", it's Nader from The Founders Group. " + line + " Have you got two minutes?"
	}
	switch {
	case r.Status == "engaged" || r.Status == "nurtured":
		p.Next, p.NextLabel = "reply", "Reply and book"
	case r.Phone != "":
		p.Next, p.NextLabel = "call", "Call now"
	case r.Email != "":
		p.Next, p.NextLabel = "email", "Email"
	case r.LinkedIn != "":
		p.Next, p.NextLabel = "linkedin", "Connect on LinkedIn"
	case r.Person != "":
		p.Next, p.NextLabel = "find_contact", "Find email and phone"
	default:
		p.Next, p.NextLabel = "research", "Find the buyer"
	}
	return p
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	// Keep "I" and company names as written; only lower a leading "Congrats".
	if strings.HasPrefix(s, "Congrats") {
		return "c" + s[1:]
	}
	return s
}
