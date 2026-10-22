// Package notify is how the OS speaks up: phone and desktop pushes (Web Push,
// VAPID keys made once on this Mac), a short feed the open OS reads aloud,
// and each person's choice of what is worth a ping. Everything lives under
// people.Root(): <person>/push.json holds that person's devices and choices.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

// Kinds a person can switch on or off.
const (
	KindApproval = "approval" // an agent needs a decision
	KindOutbox   = "outbox"   // an email waits for your OK
	KindReply    = "reply"    // a pipeline contact wrote back
	KindSent     = "sent"     // a follow-up went out
	KindAgent    = "agent"    // an agent finished a task
	KindBrief    = "brief"    // the morning summary
	KindTest     = "test"
)

// Kinds lists the switchable kinds in the order the page shows them.
var Kinds = []string{KindApproval, KindOutbox, KindReply, KindSent, KindAgent, KindBrief}

// Event is one thing worth telling someone.
type Event struct {
	ID    string    `json:"id"`
	Kind  string    `json:"kind"`
	Title string    `json:"title"`
	Body  string    `json:"body"`
	URL   string    `json:"url"`
	At    time.Time `json:"at"`
	// For limits the event to one person (a test, their brief); empty is everyone.
	For string `json:"for,omitempty"`
}

// Device is one browser or phone that said yes to notifications.
type Device struct {
	Endpoint string    `json:"endpoint"`
	P256dh   string    `json:"p256dh"`
	Auth     string    `json:"auth"`
	Label    string    `json:"label"`
	AddedAt  time.Time `json:"addedAt"`
}

// Prefs is one person's push.json.
type Prefs struct {
	Devices []Device        `json:"devices"`
	Off     map[string]bool `json:"off,omitempty"` // kinds switched off
	// BriefHour is the local hour the morning summary goes out (default 8).
	BriefHour *int   `json:"briefHour,omitempty"`
	LastBrief string `json:"lastBrief,omitempty"` // YYYY-MM-DD
	Voice     string `json:"voice,omitempty"`     // ElevenLabs voice id; empty is the OS default
}

func (p Prefs) Wants(kind string) bool { return kind == KindTest || !p.Off[kind] }

func (p Prefs) Hour() int {
	if p.BriefHour != nil && *p.BriefHour >= 0 && *p.BriefHour <= 23 {
		return *p.BriefHour
	}
	return 8
}

var mu sync.Mutex

func prefsPath(id string) string { return filepath.Join(people.Dir(id), "push.json") }

// Load reads a person's devices and choices (empty when none yet).
func Load(id string) Prefs {
	mu.Lock()
	defer mu.Unlock()
	return load(id)
}

func load(id string) Prefs {
	var p Prefs
	if raw, err := os.ReadFile(prefsPath(id)); err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

func save(id string, p Prefs) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	path := prefsPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Update changes a person's prefs under the lock.
func Update(id string, fn func(*Prefs)) (Prefs, error) {
	mu.Lock()
	defer mu.Unlock()
	p := load(id)
	fn(&p)
	return p, save(id, p)
}

// AddDevice keeps one entry per endpoint.
func AddDevice(id string, d Device) (Prefs, error) {
	if !strings.HasPrefix(d.Endpoint, "https://") || d.P256dh == "" || d.Auth == "" {
		return Prefs{}, errors.New("that is not a push subscription")
	}
	return Update(id, func(p *Prefs) {
		kept := p.Devices[:0]
		for _, x := range p.Devices {
			if x.Endpoint != d.Endpoint {
				kept = append(kept, x)
			}
		}
		p.Devices = append(kept, d)
	})
}

func RemoveDevice(id, endpoint string) (Prefs, error) {
	return Update(id, func(p *Prefs) {
		kept := p.Devices[:0]
		for _, x := range p.Devices {
			if endpoint != "" && x.Endpoint != endpoint {
				kept = append(kept, x)
			}
		}
		p.Devices = kept
	})
}

// People lists every person folder (IDs), whoever has prefs or not.
func People() []string {
	entries, err := os.ReadDir(people.Root())
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

// ---- VAPID ---------------------------------------------------------------

type vapid struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

var (
	vapidOnce sync.Once
	vapidKeys vapid
	vapidErr  error
)

func vapidPath() string {
	if p := os.Getenv("FOUNDEROS_VAPID_FILE"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(people.Root()), "vapid.json")
}

// Keys returns this OS's VAPID keys, making them the first time.
func Keys() (public, private string, err error) {
	vapidOnce.Do(func() {
		path := vapidPath()
		if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &vapidKeys) == nil && vapidKeys.Public != "" {
			return
		}
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			vapidErr = err
			return
		}
		vapidKeys = vapid{Public: pub, Private: priv}
		raw, _ := json.Marshal(vapidKeys)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			vapidErr = err
			return
		}
		vapidErr = os.WriteFile(path, raw, 0o600)
	})
	return vapidKeys.Public, vapidKeys.Private, vapidErr
}

// ---- sending -------------------------------------------------------------

// push is swapped in tests.
var push = func(ctx context.Context, msg []byte, d Device) (int, error) {
	pub, priv, err := Keys()
	if err != nil {
		return 0, err
	}
	res, err := webpush.SendNotificationWithContext(ctx, msg, &webpush.Subscription{
		Endpoint: d.Endpoint, Keys: webpush.Keys{P256dh: d.P256dh, Auth: d.Auth},
	}, &webpush.Options{
		Subscriber: contact(), VAPIDPublicKey: pub, VAPIDPrivateKey: priv,
		TTL: 6 * 3600, Urgency: webpush.UrgencyHigh,
	})
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	return res.StatusCode, nil
}

func contact() string {
	if e := people.OwnerEmail(); e != "" {
		return e
	}
	return "https://os.thefoundersgroup.net"
}

// SendTo pushes one event to every device of one person who wants its kind.
// Dead devices (404/410) are dropped. It returns how many devices took it.
func SendTo(ctx context.Context, id string, e Event) (int, error) {
	p := Load(id)
	if !p.Wants(e.Kind) || len(p.Devices) == 0 {
		return 0, nil
	}
	msg, _ := json.Marshal(map[string]any{
		"title": e.Title, "body": e.Body, "tag": e.ID, "icon": "/icon-192.png",
		"data": map[string]string{"url": e.URL, "kind": e.Kind},
	})
	ok := 0
	var errs []error
	for _, d := range p.Devices {
		code, err := push(ctx, msg, d)
		switch {
		case err != nil:
			errs = append(errs, err)
		case code == http.StatusGone || code == http.StatusNotFound:
			_, _ = RemoveDevice(id, d.Endpoint)
		case code >= 300:
			errs = append(errs, fmt.Errorf("%s answered %d", host(d.Endpoint), code))
		default:
			ok++
		}
	}
	return ok, errors.Join(errs...)
}

func host(endpoint string) string {
	s := strings.TrimPrefix(endpoint, "https://")
	if i := strings.IndexByte(s, '/'); i > 0 {
		s = s[:i]
	}
	return s
}

// Publish records an event in the feed and pushes it to everyone it is for.
func Publish(ctx context.Context, e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	feed.add(e)
	ids := People()
	if e.For != "" {
		ids = []string{e.For}
	}
	for _, id := range ids {
		if _, err := SendTo(ctx, id, e); err != nil {
			slog.Warn("founderos notify: push failed", "kind", e.Kind, "err", err)
		}
	}
}

// ---- feed ----------------------------------------------------------------

// The feed is what the open OS reads aloud: the last 100 events in memory.
type ring struct {
	mu  sync.Mutex
	evs []Event
}

var feed ring

func (r *ring) add(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
	if len(r.evs) > 100 {
		r.evs = r.evs[len(r.evs)-100:]
	}
}

// Feed returns events after `after` meant for this person, oldest first.
func Feed(id string, after time.Time) []Event {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	out := []Event{}
	for _, e := range feed.evs {
		if e.At.After(after) && (e.For == "" || e.For == id) {
			out = append(out, e)
		}
	}
	return out
}

// SetPushForTest swaps the sender and returns the restore.
func SetPushForTest(f func(ctx context.Context, msg []byte, d Device) (int, error)) func() {
	old := push
	push = f
	return func() { push = old }
}
