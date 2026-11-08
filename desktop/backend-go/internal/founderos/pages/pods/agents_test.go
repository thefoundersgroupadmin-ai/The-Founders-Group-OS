package pods

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// fakeBoard records what provisioning asked the task board for.
type fakeBoard struct {
	projects     map[string]string // idempotency key → id
	agents       map[string]string // worker id → agent id
	instructions map[string]string // agent id → AGENTS.md
	routines     map[string]BoardRoutine
	triggers     map[string][]string // routine id → cron@tz
	triggerOf    map[string]string   // trigger id → routine id
	n            int
}

func newFakeBoard() *fakeBoard {
	return &fakeBoard{projects: map[string]string{}, agents: map[string]string{}, instructions: map[string]string{},
		routines: map[string]BoardRoutine{}, triggers: map[string][]string{}, triggerOf: map[string]string{}}
}

func (b *fakeBoard) id(p string) string { b.n++; return p + "-" + string(rune('a'+b.n)) }

func (b *fakeBoard) EnsureProject(_ context.Context, key, name, _ string) (string, error) {
	if id, ok := b.projects[key]; ok {
		return id, nil
	}
	b.projects[key] = b.id("proj")
	return b.projects[key], nil
}

func (b *fakeBoard) EnsureAgent(_ context.Context, workerID, name, instructions string) (string, error) {
	if id, ok := b.agents[workerID]; ok {
		return id, nil
	}
	b.agents[workerID] = b.id("agent")
	b.instructions[b.agents[workerID]] = instructions
	return b.agents[workerID], nil
}

func (b *fakeBoard) PutInstructions(_ context.Context, agentID, content string) error {
	b.instructions[agentID] = content
	return nil
}

func (b *fakeBoard) CreateRoutine(_ context.Context, r BoardRoutine) (string, error) {
	id := b.id("routine")
	b.routines[id] = r
	return id, nil
}

func (b *fakeBoard) UpdateRoutine(_ context.Context, id string, r BoardRoutine) error {
	b.routines[id] = r
	return nil
}

func (b *fakeBoard) AddScheduleTrigger(_ context.Context, routineID, cron, tz string) (string, error) {
	b.triggers[routineID] = append(b.triggers[routineID], cron+"@"+tz)
	id := b.id("trigger")
	b.triggerOf[id] = routineID
	return id, nil
}

func (b *fakeBoard) UpdateScheduleTrigger(_ context.Context, triggerID, cron, tz string) error {
	b.triggers[b.triggerOf[triggerID]] = []string{cron + "@" + tz}
	return nil
}

func readyPod(t *testing.T, s *Store, name string) Pod {
	t.Helper()
	ctx := context.Background()
	p := newPod(t, s, name)
	_ = s.SetSender(ctx, p.Slug, Sender{Name: "Sam Reed", Email: "sam@" + strings.ToLower(name) + "-mail.example"})
	_ = s.SetDoc(ctx, p.Slug, "icp", "Warehouse operators in the US, 50-500 staff, opening new sites")
	_ = s.SetDoc(ctx, p.Slug, "voice", "Plain, warm, specific; no jargon")
	_ = s.SetDoc(ctx, p.Slug, "product", "Northwind: one live stock count across every warehouse")
	_ = s.SetDoc(ctx, p.Slug, "exclusions", "Existing customers: Oakfield, Tamworth")
	p, _ = s.Get(ctx, p.Slug)
	return p
}

func TestProvisioningGivesAPodItsOwnScopedAgentAndRoutinesOnce(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	b := newFakeBoard()
	a, other := readyPod(t, s, "Brightvale"), readyPod(t, s, "Kestrel")
	_ = s.SetDoc(ctx, other.Slug, "icp", "KESTREL-SECRET-ICP")

	bind, err := s.Provision(ctx, b, a.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if bind.ProjectID == "" || bind.AgentID == "" || bind.ResearchRoutineID == "" || bind.OutreachRoutineID == "" {
		t.Fatalf("binding incomplete: %+v", bind)
	}
	instr := b.instructions[bind.AgentID]
	research, outreach := b.routines[bind.ResearchRoutineID], b.routines[bind.OutreachRoutineID]
	all := instr + research.Description + outreach.Description
	for _, want := range []string{"Brightvale", "Sam Reed", "Warehouse operators", "one live stock count", "Oakfield", `"outbox"`, `"prospects"`, "no thanks"} {
		if !strings.Contains(all, want) {
			t.Errorf("pod context lacks %q", want)
		}
	}
	for _, leak := range []string{"Nader", "Founders Group", "3,850", "handover", "KESTREL-SECRET-ICP", "--workspace founderos"} {
		if strings.Contains(all, leak) {
			t.Errorf("pod context leaks %q", leak)
		}
	}
	if research.ProjectID != bind.ProjectID || research.AssigneeAgentID != bind.AgentID || outreach.ProjectID != bind.ProjectID {
		t.Fatalf("routines not bound to the pod's project and agent: %+v %+v", research, outreach)
	}
	if research.Status != "paused" {
		t.Fatalf("a pod in setup has active routines: %s", research.Status)
	}
	if got := b.triggers[bind.ResearchRoutineID]; len(got) != 1 || !strings.HasSuffix(got[0], "@Europe/London") {
		t.Fatalf("research trigger: %v", got)
	}

	// Again: same project, agent and routines; no new triggers; changed docs flow in.
	_ = s.SetDoc(ctx, a.Slug, "product", "Northwind v2: live counts plus reorder alerts")
	again, err := s.Provision(ctx, b, a.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if again != bind.withHash(again.BriefHash) || len(b.routines) != 2 || len(b.triggers[bind.ResearchRoutineID]) != 1 {
		t.Fatalf("not idempotent: %+v vs %+v, %d routines", again, bind, len(b.routines))
	}
	if !strings.Contains(b.routines[bind.OutreachRoutineID].Description+b.instructions[bind.AgentID], "reorder alerts") {
		t.Fatal("edited document did not reach the agent")
	}
}

func TestPodRoutinesRunOnlyWhileThePodDoes(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	b := newFakeBoard()
	p := readyPod(t, s, "Brightvale")
	bind, _ := s.Provision(ctx, b, p.Slug)
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Provision(ctx, b, p.Slug); err != nil {
		t.Fatal(err)
	}
	if b.routines[bind.ResearchRoutineID].Status != "active" || b.routines[bind.OutreachRoutineID].Status != "active" {
		t.Fatal("dry-run pod's routines not active")
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusPaused, "client on holiday"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Provision(ctx, b, p.Slug); err != nil {
		t.Fatal(err)
	}
	if b.routines[bind.ResearchRoutineID].Status != "paused" {
		t.Fatal("paused pod still researching")
	}
	got, err := s.PodForProject(ctx, bind.ProjectID)
	if err != nil || got.Slug != p.Slug {
		t.Fatalf("project → pod: %+v %v", got, err)
	}
	if _, err := s.PodForProject(ctx, "some-other-project"); err != ErrNotFound {
		t.Fatalf("unknown project: %v", err)
	}
}

type flakyTriggers struct {
	*fakeBoard
	fail int
}

func (f *flakyTriggers) AddScheduleTrigger(ctx context.Context, id, cron, tz string) (string, error) {
	if f.fail > 0 {
		f.fail--
		return "", errors.New("board timeout")
	}
	return f.fakeBoard.AddScheduleTrigger(ctx, id, cron, tz)
}

// Codex M1 review #10: a routine created without its schedule gets one on
// the next provisioning instead of staying unscheduled forever.
func TestAMissingScheduleIsRepairedOnTheNextSync(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	b := &flakyTriggers{fakeBoard: newFakeBoard(), fail: 1}
	p := readyPod(t, s, "Brightvale")
	if _, err := s.Provision(ctx, b, p.Slug); err == nil {
		t.Fatal("a failed schedule was not reported")
	}
	bind, err := s.Provision(ctx, b, p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.triggers[bind.ResearchRoutineID]) != 1 || len(b.triggers[bind.OutreachRoutineID]) != 1 || len(b.routines) != 2 {
		t.Fatalf("schedules after repair: %v (%d routines)", b.triggers, len(b.routines))
	}
	if _, err := s.Provision(ctx, b, p.Slug); err != nil || len(b.triggers[bind.ResearchRoutineID]) != 1 {
		t.Fatalf("schedule added twice: %v %v", b.triggers, err)
	}
}

// Codex fix review #10: schedules follow the pod's timezone and weekends.
func TestSchedulesFollowTimezoneAndWeekendChanges(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	b := newFakeBoard()
	p := readyPod(t, s, "Brightvale")
	bind, err := s.Provision(ctx, b, p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSettings(ctx, p.Slug, Settings{ApprovalPolicy: "client_approves_each", Timezone: "America/New_York",
		SendWindowStart: 9, SendWindowEnd: 17, SendWeekends: true, FirstEmailDailyCap: 20, FollowUpDailyCap: 40}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Provision(ctx, b, p.Slug); err != nil {
		t.Fatal(err)
	}
	got := b.triggers[bind.ResearchRoutineID]
	if len(got) != 1 || got[0] != "0 7 * * *@America/New_York" {
		t.Fatalf("research schedule after change: %v", got)
	}
}
