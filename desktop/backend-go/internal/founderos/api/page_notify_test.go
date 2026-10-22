package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/notify"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
)

func ptr[T any](v T) *T { return &v }

type fakeNotify struct {
	approvals []paperclip.Interaction
	box       outbox.Page
	replies   []leadReply
	done      []paperclip.Issue
	calls     int
	boardErr  error
}

func (f *fakeNotify) sources() notifySources {
	return notifySources{
		Approvals:  func(context.Context) ([]paperclip.Interaction, error) { return f.approvals, f.boardErr },
		Outbox:     func(context.Context) (*outbox.Page, error) { return &f.box, nil },
		Replies:    func(context.Context) ([]leadReply, error) { return f.replies, nil },
		Done:       func(context.Context) ([]paperclip.Issue, error) { return f.done, f.boardErr },
		CallsToday: func(context.Context, time.Time) (int, error) { return f.calls, nil },
	}
}

func TestTheWatcherTellsOnlyWhatIsNewAndNeverTheBacklog(t *testing.T) {
	t.Setenv("FOUNDEROS_PEOPLE_DIR", filepath.Join(t.TempDir(), "people"))
	now := time.Now()
	f := &fakeNotify{
		approvals: []paperclip.Interaction{{ID: "i1", Title: "Publish the VSL?"}},
		box:       outbox.Page{Pending: []outbox.Item{{ID: "o1", To: "a@x.co", Subject: "Hi", Status: "pending"}}},
		done:      []paperclip.Issue{{ID: "d1", Title: "Old work", Status: "done"}},
	}
	seen := &seenState{Kinds: map[string]map[string]time.Time{}}
	if evs := notifyPass(context.Background(), f.sources(), seen, now); len(evs) != 0 {
		t.Fatalf("first pass is the baseline, got %+v", evs)
	}

	f.approvals = append(f.approvals, paperclip.Interaction{ID: "i2", IssueTitle: "Approve the ad budget"})
	f.box.Pending = append(f.box.Pending, outbox.Item{ID: "o2", To: "dana@reyes.co", ToName: ptr("Dana Reyes"), Subject: "Following up", Status: "pending"})
	f.box.Recent = []outbox.Item{{ID: "s1", To: "b@x.co", Subject: "Checking in", Status: "sent"}}
	f.replies = []leadReply{{Key: "dana@reyes.co|t1", Name: "Dana Reyes", Company: "Reyes Co", Subject: "Re: call", At: now}}
	f.done = append(f.done, paperclip.Issue{ID: "d2", Title: "Draft the newsletter", Status: "done", AssigneeName: ptr("Writer")})
	evs := notifyPass(context.Background(), f.sources(), seen, now)

	got := map[string]notify.Event{}
	for _, e := range evs {
		got[e.Kind] = e
	}
	// sent and reply had no baseline yet in pass one (empty), so their first items still count
	for kind, want := range map[string]string{
		notify.KindApproval: "Approve the ad budget",
		notify.KindOutbox:   "To Dana Reyes: Following up",
		notify.KindAgent:    "Draft the newsletter",
	} {
		if got[kind].Body != want {
			t.Errorf("%s: body %q, want %q (all: %+v)", kind, got[kind].Body, want, evs)
		}
	}
	if got[notify.KindAgent].Title != "Writer finished" {
		t.Errorf("agent title %q", got[notify.KindAgent].Title)
	}
	if got[notify.KindReply].Title != "Reply from Dana Reyes (Reyes Co)" || got[notify.KindSent].Body != "To b@x.co: Checking in" {
		t.Errorf("reply/sent: %+v / %+v", got[notify.KindReply], got[notify.KindSent])
	}
	if again := notifyPass(context.Background(), f.sources(), seen, now); len(again) != 0 {
		t.Fatalf("nothing new must say nothing, got %+v", again)
	}
}

func TestABoardOutageIsNotABaselineAndABurstCollapses(t *testing.T) {
	now := time.Now()
	f := &fakeNotify{boardErr: errors.New("down")}
	seen := &seenState{Kinds: map[string]map[string]time.Time{}}
	notifyPass(context.Background(), f.sources(), seen, now)
	if _, ok := seen.Kinds[notify.KindApproval]; ok {
		t.Fatal("an unreadable board must not record a baseline")
	}
	f.boardErr = nil
	notifyPass(context.Background(), f.sources(), seen, now) // baseline: empty
	for i := 0; i < 7; i++ {
		f.approvals = append(f.approvals, paperclip.Interaction{ID: string(rune('a' + i)), Title: "x"})
	}
	evs := notifyPass(context.Background(), f.sources(), seen, now)
	if len(evs) != 3 || evs[2].Body != "and 5 more" {
		t.Fatalf("want two plus a summary, got %+v", evs)
	}
}

func TestTheMorningBriefSaysTheDayInPlainWords(t *testing.T) {
	now := time.Now()
	f := &fakeNotify{
		calls:     2,
		approvals: []paperclip.Interaction{{ID: "i1"}},
		box: outbox.Page{
			Pending: []outbox.Item{{ID: "o1", Status: "pending"}, {ID: "o2", Status: "failed"}},
			Recent:  []outbox.Item{{ID: "s1", Status: "sent", SentAt: ptr(now.Add(-time.Hour))}, {ID: "s2", Status: "sent", SentAt: ptr(now.Add(-48 * time.Hour))}},
		},
		replies: []leadReply{{Name: "Dana Reyes", At: now.Add(-time.Hour)}, {Name: "Old Lead", At: now.Add(-72 * time.Hour)}},
	}
	got := briefFrom(context.Background(), f.sources(), "Nader Ryuki", now)
	want := "Good morning, Nader. You have 2 calls today. 1 lead replied since yesterday: Dana. 1 decision waits for you in Approvals. 1 email waits for your OK in the Outbox. 1 follow-up went out in the last day."
	if got != want {
		t.Fatalf("brief:\n got %q\nwant %q", got, want)
	}
	if quiet := briefFrom(context.Background(), (&fakeNotify{}).sources(), "", now); quiet != "Good morning. No calls booked today. Nothing else needs you right now." {
		t.Fatalf("quiet brief %q", quiet)
	}
}

func TestPhonesSubscribeChoosePingsAndGetATest(t *testing.T) {
	peopleSetup(t)
	t.Setenv("FOUNDEROS_VAPID_FILE", filepath.Join(t.TempDir(), "vapid.json"))
	var pushed []string
	restore := notify.SetPushForTest(func(_ context.Context, msg []byte, d notify.Device) (int, error) {
		pushed = append(pushed, d.Label+":"+string(msg))
		return http.StatusCreated, nil
	})
	defer restore()
	r := asUser(t, &Deps{}, karim)

	w := do(t, r, http.MethodGet, "/api/founderos/pages/notify", "")
	var st notifyStatus
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.PublicKey == "" || len(st.Devices) != 0 || st.BriefHour != 8 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/notify/test", "{}"); w.Code != http.StatusBadGateway {
		t.Fatalf("a test with no device must say so, got %d", w.Code)
	}
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/notify/devices", `{"endpoint":"http://evil","keys":{"p256dh":"k","auth":"a"}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("non-https endpoint accepted: %d", w.Code)
	}
	sub := `{"endpoint":"https://web.push.apple.com/abc","keys":{"p256dh":"k","auth":"a"},"label":"iPhone"}`
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/notify/devices", sub); w.Code != http.StatusOK {
		t.Fatalf("subscribe %d %s", w.Code, w.Body)
	}
	do(t, r, http.MethodPost, "/api/founderos/pages/notify/devices", sub) // same phone twice is one device
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/notify/test", "{}"); w.Code != http.StatusOK || len(pushed) != 1 || !strings.HasPrefix(pushed[0], "iPhone:") {
		t.Fatalf("test push %d %v", w.Code, pushed)
	}

	w = do(t, r, http.MethodPut, "/api/founderos/pages/notify/prefs", `{"kinds":{"sent":false},"briefHour":7}`)
	if json.Unmarshal(w.Body.Bytes(), &st) != nil || st.BriefHour != 7 {
		t.Fatalf("prefs %s", w.Body)
	}
	for _, k := range st.Kinds {
		if k.On == (k.Kind == notify.KindSent) {
			t.Errorf("%s on=%v", k.Kind, k.On)
		}
	}
	pushed = nil
	notify.Publish(context.Background(), notify.Event{ID: "e1", Kind: notify.KindSent, Title: "Follow-up sent"})
	notify.Publish(context.Background(), notify.Event{ID: "e2", Kind: notify.KindReply, Title: "Reply"})
	if len(pushed) != 1 || !strings.Contains(pushed[0], `"Reply"`) {
		t.Fatalf("switched-off kind still pushed: %v", pushed)
	}
	w = do(t, r, http.MethodGet, "/api/founderos/pages/notify/feed?after="+time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), "")
	if !strings.Contains(w.Body.String(), `"e2"`) {
		t.Fatalf("feed %s", w.Body)
	}

	if w := do(t, r, http.MethodDelete, "/api/founderos/pages/notify/devices", `{"endpoint":"https://web.push.apple.com/abc"}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "apple") {
		t.Fatalf("unsubscribe %d %s", w.Code, w.Body)
	}
}

func TestADeadPhoneIsDropped(t *testing.T) {
	peopleSetup(t)
	restore := notify.SetPushForTest(func(context.Context, []byte, notify.Device) (int, error) { return http.StatusGone, nil })
	defer restore()
	if _, err := notify.AddDevice("p1", notify.Device{Endpoint: "https://fcm.googleapis.com/x", P256dh: "k", Auth: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := notify.SendTo(context.Background(), "p1", notify.Event{Kind: notify.KindTest}); err != nil {
		t.Fatal(err)
	}
	if n := len(notify.Load("p1").Devices); n != 0 {
		t.Fatalf("gone device kept: %d", n)
	}
}

func TestSpeakUsesElevenLabsAndFallsBackHonestly(t *testing.T) {
	var gotKey, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("xi-api-key"), r.URL.Path
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		gotBody = string(raw[:n])
		if strings.Contains(gotBody, "spent") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":{"status":"quota_exceeded"}}`))
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3audio"))
	}))
	defer srv.Close()
	old := elevenLabsURL
	elevenLabsURL = srv.URL
	defer func() { elevenLabsURL = old }()
	t.Setenv("FOUNDEROS_PEOPLE_DIR", t.TempDir())

	r := asUser(t, &Deps{}, karim)
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/voice/speak", `{"text":"hi"}`); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"fallback":true`) {
		t.Fatalf("no key must fall back: %d %s", w.Code, w.Body)
	}
	t.Setenv("ELEVENLABS_API_KEY", "sk_test")
	r = asUser(t, &Deps{Resolver: connectors.Resolver{}}, karim)
	w := do(t, r, http.MethodPost, "/api/founderos/pages/voice/speak", `{"text":"**Two** calls today."}`)
	if w.Code != http.StatusOK || w.Body.String() != "ID3audio" || gotKey != "sk_test" || !strings.HasPrefix(gotPath, "/v1/text-to-speech/") {
		t.Fatalf("speak %d %q key=%q path=%q", w.Code, w.Body, gotKey, gotPath)
	}
	if !strings.Contains(gotBody, `"text":"Two calls today."`) || !strings.Contains(gotBody, "eleven_flash_v2_5") {
		t.Fatalf("body %s", gotBody)
	}
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/voice/speak", `{"text":"spent"}`); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "used up") {
		t.Fatalf("quota %d %s", w.Code, w.Body)
	}
}

func TestLongRepliesAreCutAtASentence(t *testing.T) {
	long := strings.Repeat("This is one sentence. ", 80)
	got := speakText(long)
	if len(got) > speakMax+40 || !strings.HasSuffix(got, ". The rest is on screen.") {
		t.Fatalf("%d %q", len(got), got[len(got)-40:])
	}
}

func TestEachPersonPicksTheirOwnVoice(t *testing.T) {
	peopleSetup(t)
	var spokeWith string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/voices" {
			_, _ = w.Write([]byte(`{"voices":[
				{"voice_id":"sarah1","name":"Sarah - Mature, Reassuring, Confident","labels":{"gender":"female","accent":"american","age":"young"},"preview_url":"https://cdn/s.mp3"},
				{"voice_id":"george1","name":"George - Warm, Captivating Storyteller","labels":{"gender":"male","accent":"british","age":"middle_aged"}}]}`))
			return
		}
		spokeWith = strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")
		_, _ = w.Write([]byte("ID3"))
	}))
	defer srv.Close()
	old := elevenLabsURL
	elevenLabsURL = srv.URL
	defer func() { elevenLabsURL = old }()
	voiceCache.Lock()
	voiceCache.list = nil
	voiceCache.Unlock()
	t.Setenv("ELEVENLABS_API_KEY", "sk_test")
	t.Setenv("ELEVENLABS_VOICE_ID", "george1")
	r := asUser(t, &Deps{}, karim)

	w := do(t, r, http.MethodGet, "/api/founderos/pages/voice/voices", "")
	var got struct {
		Voices  []voiceOption `json:"voices"`
		Current string        `json:"current"`
	}
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Voices) != 2 || got.Current != "george1" {
		t.Fatalf("voices %s", w.Body)
	}
	if v := got.Voices[0]; v.Name != "Sarah" || v.Description != "Mature, Reassuring, Confident" || v.Accent != "american" || v.Preview != "https://cdn/s.mp3" {
		t.Fatalf("sarah %+v", v)
	}
	if w := do(t, r, http.MethodPut, "/api/founderos/pages/voice", `{"voiceId":"made-up"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown voice accepted: %d", w.Code)
	}
	if w := do(t, r, http.MethodPut, "/api/founderos/pages/voice", `{"voiceId":"sarah1"}`); w.Code != http.StatusOK {
		t.Fatalf("pick %d %s", w.Code, w.Body)
	}
	do(t, r, http.MethodPost, "/api/founderos/pages/voice/speak", `{"text":"hi"}`)
	if !strings.HasPrefix(spokeWith, "sarah1") {
		t.Fatalf("spoke with %q", spokeWith)
	}
	do(t, asUser(t, &Deps{}, ownerUser), http.MethodPost, "/api/founderos/pages/voice/speak", `{"text":"hi"}`)
	if !strings.HasPrefix(spokeWith, "george1") {
		t.Fatalf("someone else's pick leaked: %q", spokeWith)
	}
}
