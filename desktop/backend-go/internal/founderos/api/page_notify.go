package api

// The OS talks: phone pushes when something needs you (notify), a feed the
// open OS reads aloud, the morning brief, and a natural voice (ElevenLabs,
// falling back to the browser's own voice when the key is missing or the
// month's characters are spent).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/notify"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

func init() { RegisterPage(registerNotify) }

type notifyStatus struct {
	PublicKey string          `json:"publicKey"`
	Devices   []notifyDevice  `json:"devices"`
	Kinds     []notifyKind    `json:"kinds"`
	BriefHour int             `json:"briefHour"`
	Voice     notifyVoiceInfo `json:"voice"`
}

type notifyDevice struct {
	Endpoint string    `json:"endpoint"`
	Label    string    `json:"label"`
	AddedAt  time.Time `json:"addedAt"`
}

type notifyKind struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	On    bool   `json:"on"`
}

type notifyVoiceInfo struct {
	Natural bool   `json:"natural"` // an ElevenLabs key is set
	Detail  string `json:"detail"`
}

var notifyKindLabels = map[string]string{
	notify.KindApproval: "An agent needs your decision",
	notify.KindOutbox:   "An email waits for your OK",
	notify.KindReply:    "A lead replied",
	notify.KindSent:     "A follow-up went out",
	notify.KindAgent:    "An agent finished a task",
	notify.KindBrief:    "Morning brief",
}

func notifyStatusOf(d *Deps, id string) notifyStatus {
	pub, _, _ := notify.Keys()
	p := notify.Load(id)
	st := notifyStatus{PublicKey: pub, Devices: []notifyDevice{}, BriefHour: p.Hour()}
	for _, dv := range p.Devices {
		st.Devices = append(st.Devices, notifyDevice{Endpoint: dv.Endpoint, Label: dv.Label, AddedAt: dv.AddedAt})
	}
	for _, k := range notify.Kinds {
		st.Kinds = append(st.Kinds, notifyKind{Kind: k, Label: notifyKindLabels[k], On: p.Wants(k)})
	}
	if voiceKey(d) != "" {
		st.Voice = notifyVoiceInfo{Natural: true, Detail: "natural voice (ElevenLabs)"}
	} else {
		st.Voice = notifyVoiceInfo{Detail: "browser voice: add ELEVENLABS_API_KEY for a natural one"}
	}
	return st
}

func registerNotify(s *gin.RouterGroup, d *Deps) {
	startNotifyWatch(d)

	s.GET("/pages/notify", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, notifyStatusOf(d, p.ID))
	})
	s.POST("/pages/notify/devices", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256dh string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
			Label string `json:"label"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "send the browser's push subscription"})
			return
		}
		label := strings.TrimSpace(body.Label)
		if label == "" {
			label = "a browser"
		}
		if len(label) > 60 {
			label = label[:60]
		}
		if _, err := notify.AddDevice(p.ID, notify.Device{Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth,
			Label: label, AddedAt: time.Now()}); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, notifyStatusOf(d, p.ID))
	})
	s.DELETE("/pages/notify/devices", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Endpoint == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "which device?"})
			return
		}
		if _, err := notify.RemoveDevice(p.ID, body.Endpoint); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, notifyStatusOf(d, p.ID))
	})
	s.PUT("/pages/notify/prefs", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		var body struct {
			Kinds     map[string]bool `json:"kinds"`
			BriefHour *int            `json:"briefHour"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if body.BriefHour != nil && (*body.BriefHour < 0 || *body.BriefHour > 23) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the brief hour is 0 to 23"})
			return
		}
		if _, err := notify.Update(p.ID, func(pr *notify.Prefs) {
			if pr.Off == nil {
				pr.Off = map[string]bool{}
			}
			for k, on := range body.Kinds {
				if _, known := notifyKindLabels[k]; known {
					if on {
						delete(pr.Off, k)
					} else {
						pr.Off[k] = true
					}
				}
			}
			if body.BriefHour != nil {
				pr.BriefHour = body.BriefHour
			}
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, notifyStatusOf(d, p.ID))
	})
	s.POST("/pages/notify/test", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		e := notify.Event{ID: fmt.Sprintf("test-%d", time.Now().UnixNano()), Kind: notify.KindTest,
			Title: "TFG OS", Body: "Notifications are on. This is how the OS will reach you.", URL: "/os", For: p.ID}
		n, err := notify.SendTo(c.Request.Context(), p.ID, e)
		if n == 0 && err == nil {
			err = errors.New("no device is turned on yet")
		}
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"sent": n, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"sent": n})
	})
	// The open OS reads new events aloud: everything after ?after (RFC 3339).
	s.GET("/pages/notify/feed", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		after, err := time.Parse(time.RFC3339Nano, c.Query("after"))
		if err != nil {
			after = time.Now()
		}
		c.JSON(http.StatusOK, gin.H{"now": time.Now().UTC().Format(time.RFC3339Nano), "events": notify.Feed(p.ID, after)})
	})
	s.GET("/pages/notify/brief", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
		defer cancel()
		name := ""
		if p, ok := people.From(c.Request.Context()); ok {
			name = p.Name
		}
		c.JSON(http.StatusOK, gin.H{"text": morningBrief(ctx, d, name, time.Now())})
	})
	s.POST("/pages/voice/speak", func(c *gin.Context) {
		var body struct {
			Text string `json:"text"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Text) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to say"})
			return
		}
		audio, err := speak(c.Request.Context(), d, body.Text, voiceFor(d, personID(c)))
		if err != nil {
			// The browser speaks instead; this is not an outage.
			c.JSON(http.StatusServiceUnavailable, gin.H{"fallback": true, "error": err.Error()})
			return
		}
		c.Data(http.StatusOK, "audio/mpeg", audio)
	})
	// The voices this ElevenLabs account can use, each with its free sample.
	s.GET("/pages/voice/voices", func(c *gin.Context) {
		list, err := voiceList(c.Request.Context(), d)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"voices": []voiceOption{}, "current": "", "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"voices": list, "current": voiceFor(d, personID(c))})
	})
	s.PUT("/pages/voice", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		var body struct {
			VoiceID string `json:"voiceId"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.VoiceID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "which voice?"})
			return
		}
		list, err := voiceList(c.Request.Context(), d)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		known := false
		for _, v := range list {
			known = known || v.ID == body.VoiceID
		}
		if !known {
			c.JSON(http.StatusBadRequest, gin.H{"error": "that voice is not on this account"})
			return
		}
		if _, err := notify.Update(p.ID, func(pr *notify.Prefs) { pr.Voice = body.VoiceID }); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"voices": list, "current": body.VoiceID})
	})
}

func personID(c *gin.Context) string {
	if p, ok := people.From(c.Request.Context()); ok {
		return p.ID
	}
	return ""
}

// defaultVoice is the OS-wide voice: ELEVENLABS_VOICE_ID, else George.
func defaultVoice(d *Deps) string {
	if v := d.Resolver.Resolve("ELEVENLABS_VOICE_ID"); v != "" {
		return v
	}
	return "JBFqnCBsd6RMkjVDRZzb" // George, a premade voice every plan can use
}

// voiceFor is the person's own pick, else the OS default.
func voiceFor(d *Deps, id string) string {
	if id != "" {
		if v := notify.Load(id).Voice; v != "" {
			return v
		}
	}
	return defaultVoice(d)
}

type voiceOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Gender      string `json:"gender"`
	Accent      string `json:"accent"`
	Age         string `json:"age"`
	Preview     string `json:"preview"`
}

var voiceCache struct {
	sync.Mutex
	at   time.Time
	list []voiceOption
}

// voiceList reads the account's voices (cached an hour; listing is free).
func voiceList(ctx context.Context, d *Deps) ([]voiceOption, error) {
	voiceCache.Lock()
	defer voiceCache.Unlock()
	if voiceCache.list != nil && time.Since(voiceCache.at) < time.Hour {
		return voiceCache.list, nil
	}
	key := voiceKey(d)
	if key == "" {
		return nil, errors.New("no ElevenLabs key")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, elevenLabsURL+"/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ElevenLabs answered %d", res.StatusCode)
	}
	var body struct {
		Voices []struct {
			ID      string            `json:"voice_id"`
			Name    string            `json:"name"`
			Labels  map[string]string `json:"labels"`
			Preview string            `json:"preview_url"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	list := []voiceOption{}
	for _, v := range body.Voices {
		name, desc, _ := strings.Cut(v.Name, " - ")
		list = append(list, voiceOption{ID: v.ID, Name: strings.TrimSpace(name), Description: strings.TrimSpace(desc),
			Gender: v.Labels["gender"], Accent: v.Labels["accent"], Age: strings.ReplaceAll(v.Labels["age"], "_", " "), Preview: v.Preview})
	}
	voiceCache.list, voiceCache.at = list, time.Now()
	return list, nil
}

// ---- voice ---------------------------------------------------------------

func voiceKey(d *Deps) string { return d.Resolver.Resolve("ELEVENLABS_API_KEY") }

// speakMax keeps one reply from spending the month: long replies are cut at a
// sentence near this many characters.
const speakMax = 900

var elevenLabsURL = "https://api.elevenlabs.io"

func speakText(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	// Markdown reads badly aloud.
	t = strings.NewReplacer("**", "", "__", "", "`", "", "# ", "", "* ", "", "- ", "").Replace(t)
	if len(t) <= speakMax {
		return t
	}
	cut := t[:speakMax]
	if i := strings.LastIndexAny(cut, ".!?"); i > speakMax/2 {
		cut = cut[:i+1]
	}
	return cut + " The rest is on screen."
}

func speak(ctx context.Context, d *Deps, text, voice string) ([]byte, error) {
	key := voiceKey(d)
	if key == "" {
		return nil, errors.New("no ElevenLabs key")
	}
	if voice == "" {
		voice = defaultVoice(d)
	}
	model := d.Resolver.Resolve("ELEVENLABS_MODEL")
	if model == "" {
		model = "eleven_flash_v2_5" // half the characters of the multilingual model
	}
	payload, _ := json.Marshal(map[string]any{"text": speakText(text), "model_id": model})
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		elevenLabsURL+"/v1/text-to-speech/"+voice+"?output_format=mp3_44100_64", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Detail struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"detail"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Detail.Status == "quota_exceeded" {
			return nil, errors.New("this month's ElevenLabs characters are used up")
		}
		return nil, fmt.Errorf("ElevenLabs answered %d %s", res.StatusCode, e.Detail.Status)
	}
	return raw, nil
}

// ---- the watcher -----------------------------------------------------------

// notifySources are what the watcher reads; tests swap them.
type notifySources struct {
	Approvals  func(ctx context.Context) ([]paperclip.Interaction, error)
	Outbox     func(ctx context.Context) (*outbox.Page, error)
	Replies    func(ctx context.Context) ([]leadReply, error)
	Done       func(ctx context.Context) ([]paperclip.Issue, error)
	CallsToday func(ctx context.Context, now time.Time) (int, error)
}

type leadReply struct {
	Key, Name, Company, Subject string
	At                          time.Time
}

var notifySourcesFor = func(d *Deps) notifySources {
	return notifySources{
		Approvals: func(ctx context.Context) ([]paperclip.Interaction, error) {
			box, err := approvalsBoardFor(d).PendingInteractions(ctx)
			if err != nil {
				return nil, err
			}
			return box.Items, nil
		},
		Outbox: func(ctx context.Context) (*outbox.Page, error) {
			if d.Pool == nil {
				return nil, errors.New("no database")
			}
			return outbox.Load(ctx, d.Pool, outboxWorkspace)
		},
		Replies: func(ctx context.Context) ([]leadReply, error) { return leadReplies(ctx, d) },
		Done: func(ctx context.Context) ([]paperclip.Issue, error) {
			list, err := paperclip.New(d.Resolver).Issues(ctx, 50)
			if err != nil {
				return nil, err
			}
			var done []paperclip.Issue
			for _, is := range list {
				if is.Status == "done" {
					done = append(done, is)
				}
			}
			return done, nil
		},
		CallsToday: func(ctx context.Context, now time.Time) (int, error) {
			list, err := calBookings(ctx, d)
			if err != nil {
				return 0, err
			}
			n := 0
			y, m, dd := now.Date()
			for _, b := range list {
				if t, err := time.Parse(time.RFC3339, b.Start); err == nil && b.Status != "cancelled" {
					ty, tm, td := t.In(now.Location()).Date()
					if ty == y && tm == m && td == dd {
						n++
					}
				}
			}
			return n, nil
		},
	}
}

// leadReplies are inbox emails from an address on the pipeline.
func leadReplies(ctx context.Context, d *Deps) ([]leadReply, error) {
	if d.Pool == nil {
		return nil, errors.New("no database")
	}
	mails, err := commsSourcesFor(d).Emails(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := d.Pool.Query(ctx, `SELECT lower(email), COALESCE(name, person, ''), COALESCE(company, '')
		FROM founderos_funnel_contacts WHERE COALESCE(email, '') <> ''`)
	if err != nil {
		return nil, err
	}
	type who struct{ name, company string }
	contacts := map[string]who{}
	for rows.Next() {
		var e, n, co string
		if rows.Scan(&e, &n, &co) == nil {
			contacts[strings.TrimSpace(e)] = who{n, co}
		}
	}
	rows.Close()
	var out []leadReply
	for _, m := range mails {
		w, ok := contacts[strings.ToLower(strings.TrimSpace(m.ReplyTo))]
		if !ok {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, m.TS)
		name := w.name
		if name == "" {
			name = m.Sender
		}
		out = append(out, leadReply{Key: strings.ToLower(m.ReplyTo) + "|" + m.TS, Name: name, Company: w.company, Subject: m.Preview, At: at})
	}
	return out, nil
}

// seen is what the watcher already told people about, kept across restarts so
// a restart never re-sends; the first pass of a kind only records.
type seenState struct {
	Kinds map[string]map[string]time.Time `json:"kinds"`
}

func seenPath() string { return filepath.Join(filepath.Dir(people.Root()), "notify-seen.json") }

func loadSeen() *seenState {
	s := &seenState{Kinds: map[string]map[string]time.Time{}}
	if raw, err := os.ReadFile(seenPath()); err == nil {
		_ = json.Unmarshal(raw, s)
	}
	if s.Kinds == nil {
		s.Kinds = map[string]map[string]time.Time{}
	}
	return s
}

func (s *seenState) save() {
	for _, m := range s.Kinds { // forget what is long gone
		for k, t := range m {
			if time.Since(t) > 30*24*time.Hour {
				delete(m, k)
			}
		}
	}
	raw, _ := json.Marshal(s)
	_ = os.MkdirAll(filepath.Dir(seenPath()), 0o700)
	_ = os.WriteFile(seenPath(), raw, 0o600)
}

// fresh records keys under kind and returns the ones not seen before. A kind
// seen for the first time returns nothing (that is the baseline).
func (s *seenState) fresh(kind string, keys []string, now time.Time) map[string]bool {
	m, had := s.Kinds[kind]
	if !had {
		m = map[string]time.Time{}
		s.Kinds[kind] = m
	}
	out := map[string]bool{}
	for _, k := range keys {
		if _, ok := m[k]; !ok && had {
			out[k] = true
		}
		m[k] = now
	}
	return out
}

func notifyClip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}

// notifyPass finds what is new since the last pass and returns the events.
func notifyPass(ctx context.Context, src notifySources, seen *seenState, now time.Time) []notify.Event {
	var evs []notify.Event
	if items, err := src.Approvals(ctx); err == nil {
		keys := make([]string, len(items))
		for i, it := range items {
			keys[i] = it.ID
		}
		fresh := seen.fresh(notify.KindApproval, keys, now)
		for _, it := range items {
			if fresh[it.ID] {
				title := it.Title
				if title == "" {
					title = it.IssueTitle
				}
				evs = append(evs, notify.Event{ID: "approval-" + it.ID, Kind: notify.KindApproval,
					Title: "Needs your decision", Body: notifyClip(title, 140), URL: "/os/approvals"})
			}
		}
	}
	if page, err := src.Outbox(ctx); err == nil {
		var pend, sent []string
		for _, it := range page.Pending {
			if it.Status == "pending" {
				pend = append(pend, it.ID)
			}
		}
		for _, it := range page.Recent {
			if it.Status == "sent" {
				sent = append(sent, it.ID)
			}
		}
		fp := seen.fresh(notify.KindOutbox, pend, now)
		fs := seen.fresh(notify.KindSent, sent, now)
		for _, it := range page.Pending {
			if fp[it.ID] {
				evs = append(evs, notify.Event{ID: "outbox-" + it.ID, Kind: notify.KindOutbox,
					Title: "Email waiting for your OK", Body: notifyClip("To "+recipient(it)+": "+it.Subject, 140), URL: "/os/outbox"})
			}
		}
		for _, it := range page.Recent {
			if fs[it.ID] {
				evs = append(evs, notify.Event{ID: "sent-" + it.ID, Kind: notify.KindSent,
					Title: "Follow-up sent", Body: notifyClip("To "+recipient(it)+": "+it.Subject, 140), URL: "/os/outbox"})
			}
		}
	}
	if replies, err := src.Replies(ctx); err == nil {
		keys := make([]string, len(replies))
		for i, r := range replies {
			keys[i] = r.Key
		}
		fresh := seen.fresh(notify.KindReply, keys, now)
		for _, r := range replies {
			if fresh[r.Key] {
				who := r.Name
				if r.Company != "" {
					who += " (" + r.Company + ")"
				}
				evs = append(evs, notify.Event{ID: "reply-" + r.Key, Kind: notify.KindReply,
					Title: "Reply from " + notifyClip(who, 60), Body: notifyClip(r.Subject, 140), URL: "/os/comms"})
			}
		}
	}
	if done, err := src.Done(ctx); err == nil {
		keys := make([]string, len(done))
		for i, is := range done {
			keys[i] = is.ID
		}
		fresh := seen.fresh(notify.KindAgent, keys, now)
		for _, is := range done {
			if fresh[is.ID] {
				who := "An agent"
				if is.AssigneeName != nil && *is.AssigneeName != "" {
					who = *is.AssigneeName
				}
				evs = append(evs, notify.Event{ID: "agent-" + is.ID, Kind: notify.KindAgent,
					Title: who + " finished", Body: notifyClip(is.Title, 140), URL: "/os/agents"})
			}
		}
	}
	return collapse(evs, now)
}

// collapse keeps a burst (say the board coming back with a backlog) from
// buzzing the phone ten times: past three of a kind, one "and N more".
func collapse(evs []notify.Event, now time.Time) []notify.Event {
	count := map[string]int{}
	for _, e := range evs {
		count[e.Kind]++
	}
	var out []notify.Event
	shown := map[string]int{}
	for _, e := range evs {
		if count[e.Kind] <= 3 {
			out = append(out, e)
			continue
		}
		shown[e.Kind]++
		switch {
		case shown[e.Kind] <= 2:
			out = append(out, e)
		case shown[e.Kind] == 3:
			out = append(out, notify.Event{ID: fmt.Sprintf("%s-more-%d", e.Kind, now.Unix()), Kind: e.Kind,
				Title: notifyKindLabels[e.Kind], Body: fmt.Sprintf("and %d more", count[e.Kind]-2), URL: e.URL})
		}
	}
	return out
}

func recipient(it outbox.Item) string {
	if it.ToName != nil && *it.ToName != "" {
		return *it.ToName
	}
	return it.To
}

// morningBrief is the spoken and pushed summary of the day.
func morningBrief(ctx context.Context, d *Deps, name string, now time.Time) string {
	return briefFrom(ctx, notifySourcesFor(d), name, now)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func briefFrom(ctx context.Context, src notifySources, name string, now time.Time) string {
	var (
		wg                     sync.WaitGroup
		approvals              []paperclip.Interaction
		page                   *outbox.Page
		replies                []leadReply
		calls                  int
		aErr, oErr, rErr, cErr error
	)
	wg.Add(4)
	go func() { defer wg.Done(); approvals, aErr = src.Approvals(ctx) }()
	go func() { defer wg.Done(); page, oErr = src.Outbox(ctx) }()
	go func() { defer wg.Done(); replies, rErr = src.Replies(ctx) }()
	go func() { defer wg.Done(); calls, cErr = src.CallsToday(ctx, now) }()
	wg.Wait()

	hello := "Good morning"
	if f := strings.Fields(name); len(f) > 0 {
		hello += ", " + f[0]
	}
	parts := []string{hello + "."}
	if cErr == nil {
		if calls == 0 {
			parts = append(parts, "No calls booked today.")
		} else {
			parts = append(parts, "You have "+plural(calls, "call", "calls")+" today.")
		}
	}
	if rErr == nil {
		n := 0
		var names []string
		for _, r := range replies {
			if now.Sub(r.At) < 24*time.Hour {
				n++
				if len(names) < 3 {
					names = append(names, strings.Fields(r.Name + " ")[0])
				}
			}
		}
		if n > 0 {
			parts = append(parts, plural(n, "lead", "leads")+" replied since yesterday: "+strings.Join(names, ", ")+".")
		}
	}
	if aErr == nil && len(approvals) > 0 {
		parts = append(parts, plural(len(approvals), "decision waits", "decisions wait")+" for you in Approvals.")
	}
	if oErr == nil && page != nil {
		pending, sent := 0, 0
		for _, it := range page.Pending {
			if it.Status == "pending" {
				pending++
			}
		}
		for _, it := range page.Recent {
			if it.Status == "sent" && it.SentAt != nil && now.Sub(*it.SentAt) < 24*time.Hour {
				sent++
			}
		}
		if pending > 0 {
			parts = append(parts, plural(pending, "email waits", "emails wait")+" for your OK in the Outbox.")
		}
		if sent > 0 {
			parts = append(parts, plural(sent, "follow-up", "follow-ups")+" went out in the last day.")
		}
	}
	if len(parts) == 2 && cErr == nil {
		parts = append(parts, "Nothing else needs you right now.")
	}
	return strings.Join(parts, " ")
}

// notifyBriefs sends each person their brief once a day at their hour.
func notifyBriefs(ctx context.Context, d *Deps, now time.Time) {
	today := now.Format("2006-01-02")
	var text string
	for _, id := range notify.People() {
		p := notify.Load(id)
		if len(p.Devices) == 0 || !p.Wants(notify.KindBrief) || p.LastBrief == today || now.Hour() < p.Hour() {
			continue
		}
		if text == "" {
			text = morningBrief(ctx, d, "", now)
		}
		_, _ = notify.Update(id, func(pr *notify.Prefs) { pr.LastBrief = today })
		notify.Publish(ctx, notify.Event{ID: "brief-" + today + "-" + id, Kind: notify.KindBrief,
			Title: "Morning brief", Body: strings.TrimPrefix(text, "Good morning. "), URL: "/os?brief=1", For: id})
	}
}

var notifyOnce sync.Once

// startNotifyWatch looks for news every two minutes. Never under go test.
func startNotifyWatch(d *Deps) {
	if testing.Testing() || os.Getenv("FOUNDEROS_NOTIFY") == "0" {
		return
	}
	notifyOnce.Do(func() {
		go func() {
			seen := loadSeen()
			t := time.NewTicker(2 * time.Minute)
			defer t.Stop()
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				now := time.Now()
				for _, e := range notifyPass(ctx, notifySourcesFor(d), seen, now) {
					notify.Publish(ctx, e)
				}
				seen.save()
				notifyBriefs(ctx, d, now)
				cancel()
				<-t.C
			}
		}()
		slog.Info("founderos notify: watching approvals, outbox, replies and agents")
	})
}
