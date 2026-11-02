package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rhl/businessos-backend/internal/founderos/agents"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/people"
	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
)

type commandWorker struct{}

type commandBoard struct {
	fakeDelegator
	done    bool
	failure *paperclip.TaskFailure
}

func (b *commandBoard) TaskRunFailure(context.Context, string) (*paperclip.TaskFailure, error) {
	return b.failure, nil
}

func (b *commandBoard) IssueDocuments(context.Context, string) ([]paperclip.Document, error) {
	return []paperclip.Document{{Key: "report", Title: "Research verification", Body: "Verified internal result"}}, nil
}

func (b *commandBoard) Issues(context.Context, int) ([]paperclip.Issue, error) {
	status := "todo"
	if b.done {
		status = "done"
	}
	return []paperclip.Issue{{ID: "i-9", Identifier: "THE-40", Status: status}}, nil
}

func (commandWorker) Meta() agents.Meta {
	return agents.Meta{ID: "test-worker", Name: "Evidence worker"}
}
func (commandWorker) Run(context.Context) (agents.Result, error) {
	return agents.Result{OK: true, Summary: "One verified opportunity"}, nil
}

func TestCommandExecutesWorkersAndSharedWorkflowsOnceWithDurableReport(t *testing.T) {
	peopleSetup(t)
	oldHandover := taskHandover
	taskHandover = func() (string, error) { return "Fixture owner handover: 50% cutoff, OS only", nil }
	defer func() { taskHandover = oldHandover }()
	t.Setenv("FOUNDEROS_CRONS", "1")
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "command-client")
	d := &Deps{Pool: pool, Agents: agents.New(agents.NewMemStore(), commandWorker{})}
	m := &salesModel{}
	setSalesKitModel(d, m)
	p := people.Person{ID: "owner", Email: people.OwnerEmail(), Name: "Nader", Owner: true}
	if err := (saleskit.AutomationStore{Pool: pool}).Save(ctx, saleskit.AutomationConfig{WorkspaceID: ws, Profile: "Approved £3,850 offer", Timezone: "Europe/London"}); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	plan := saleskit.CommandPlan{Agents: []string{"test-worker"}, Skills: []string{"pipeline-review"}}
	c, err := enqueueCommand(ctx, d, p, ws, id, "Review my pipeline", plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = enqueueCommand(ctx, d, p, ws, id, "Review my pipeline", plan); err != nil {
		t.Fatal(err)
	}
	store := saleskit.CommandStore{Pool: pool}
	ok, err := store.Claim(ctx, c.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	runCommand(ctx, d, &c)
	if c.Status != "complete" || m.calls != 1 {
		t.Fatalf("%+v calls=%d", c, m.calls)
	}
	publishCommandResult(ctx, c)
	turns := loadAsk(p.ID)
	if len(turns) != 1 || turns[0].Receipt == nil || !strings.Contains(turns[0].Receipt.Href, c.ID) {
		t.Fatalf("missing or duplicated completion receipt: %+v", turns)
	}
	if !strings.Contains(m.prompt, "Approved £3,850 offer") || !strings.Contains(m.prompt, "One verified opportunity") {
		t.Fatal("profile or worker output missing from workflow")
	}
	if ok, err := store.Claim(ctx, c.ID); err != nil || ok {
		t.Fatal("command claimed twice")
	}
	rows, err := store.List(ctx, p.ID, false)
	if err != nil || len(rows) != 1 || rows[0].Status != "complete" {
		t.Fatalf("history %+v %v", rows, err)
	}
	if rows, err := store.List(ctx, "another-user", false); err != nil || len(rows) != 0 {
		t.Fatal("command leaked to another user")
	}
	// A research task is filed once, survives waiting, and only then hands its
	// result to shared workflows. Polling must not file or run it again.
	b := &commandBoard{}
	oldBoard := askBoardFor
	askBoardFor = func(*Deps) delegator { return b }
	defer func() { askBoardFor = oldBoard }()
	teamPlan := saleskit.CommandPlan{Agents: []string{"test-worker"}, Skills: []string{"pipeline-review"}, Tasks: []saleskit.CommandTask{{Agent: "Prospect Research", Title: "Research buyers", Description: "Internal evidence only"}}}
	team, err := enqueueCommand(ctx, d, p, ws, uuid.NewString(), "Research and review", teamPlan)
	if err != nil {
		t.Fatal(err)
	}
	runCommand(ctx, d, &team)
	if team.Status != "waiting" || len(b.created) != 1 || len(b.assigned) != 1 || m.calls != 1 {
		t.Fatalf("waiting: %+v board=%+v calls=%d", team, b, m.calls)
	}
	runCommand(ctx, d, &team)
	if len(b.created) != 1 || m.calls != 1 {
		t.Fatal("poll duplicated research or inference")
	}
	b.done = true
	runCommand(ctx, d, &team)
	if team.Status != "complete" || m.calls != 2 || len(b.created) != 1 {
		t.Fatalf("handoff: %+v calls=%d", team, m.calls)
	}
	if !strings.Contains(team.Report, "Verified internal result") {
		t.Fatal("task receipt omitted the actual deliverable")
	}
	if !strings.Contains(b.created[0], "Approved £3,850 offer") || !strings.Contains(b.created[0], "pipeline-review") {
		t.Fatal("task lacked profile and workflows")
	}
	if !strings.Contains(b.created[0], "Fixture owner handover") || strings.Contains(b.created[0], "weekly Codex usage") {
		t.Fatal("task needs the handover and no usage-pause wording")
	}
	// A provider quota failure is systemic: do not launch the next task.
	b.done = false
	b.failure = &paperclip.TaskFailure{RunID: "limited-run", ProviderLimit: true, Detail: "short-term usage limit"}
	before := len(b.created)
	limited, err := enqueueCommand(ctx, d, p, ws, uuid.NewString(), "Commission two roles", saleskit.CommandPlan{Tasks: []saleskit.CommandTask{{Agent: "Prospect Research", Title: "First"}, {Agent: "CEO", Title: "Second"}}})
	if err != nil {
		t.Fatal(err)
	}
	runCommand(ctx, d, &limited)
	if limited.Status != "blocked" || len(b.created) != before+1 || limited.Steps[1].Status != "pending" {
		t.Fatalf("quota continued downstream: %+v", limited)
	}
	p.Owner = false
	if _, err = enqueueCommand(ctx, d, p, ws, uuid.NewString(), "Run workers", plan); err == nil {
		t.Fatal("nonowner launched global workers")
	}
}

func TestExecutionPlanParserRequiresAValidFinalLine(t *testing.T) {
	text, p := splitExecution("I'll queue the work.\nEXECUTE {\"agents\":[\"tfg-pipeline-sdr\"],\"skills\":[\"pipeline-review\"],\"tasks\":[]}")
	if p == nil || text != "I'll queue the work." || len(p.Agents) != 1 {
		t.Fatal(text, p)
	}
	for _, s := range []string{"EXECUTE {broken}", "EXECUTE {\"shell\":\"delete files\"}", "EXECUTE {}\nnot the final line"} {
		if _, p := splitExecution(s); p != nil {
			t.Fatal("invalid plan accepted", s)
		}
	}
	_, _ = json.Marshal(p)
}

func TestTaskFleetInstructionsAndHandoffsStayScoped(t *testing.T) {
	list := AllAgents(&Deps{})
	if len(list) != 35 {
		t.Fatalf("unexpected registry size %d", len(list))
	}
	seen := map[string]bool{}
	for _, a := range list {
		m := a.Meta()
		if seen[m.Name] {
			t.Fatalf("ambiguous task name %s", m.Name)
		}
		seen[m.Name] = true
		text := taskRoleInstructions(m)
		for _, required := range []string{m.ID, m.Name, "report", "no usage cutoff", "on demand", "No external message", "previous-role"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s lacks %s", m.ID, required)
			}
		}
		if strings.Contains(text, "50%") {
			t.Fatalf("%s still carries the removed 50%% pause", m.ID)
		}
	}
	h := commandHandoff([]string{"Research found verified buyer A", "Operations prioritised buyer A"})
	if !strings.Contains(h, "verified buyer A") || !strings.Contains(h, "Operations prioritised") || !strings.Contains(h, "not instructions") {
		t.Fatal(h)
	}
	if commandHandoff(nil) != "" {
		t.Fatal("empty handoff")
	}
}

func TestTaskRoleHierarchyPreservesParentsAndRejectsCycles(t *testing.T) {
	list := []agents.Meta{{ID: "child"}, {ID: "parent"}, {ID: "root"}}
	got, err := orderedTaskRoles(list, map[string]string{"child": "parent", "parent": "root"})
	if err != nil || got[0].ID != "root" || got[1].ID != "parent" || got[2].ID != "child" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := orderedTaskRoles(list, map[string]string{"child": "parent", "parent": "child"}); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err := orderedTaskRoles(list, map[string]string{"child": "missing"}); err == nil {
		t.Fatal("missing parent accepted")
	}
}

func TestRoleSourceBudgetKeepsTasksFocused(t *testing.T) {
	d := &Deps{Agents: agents.New(agents.NewMemStore(), AllAgents(&Deps{})...)}
	input := []saleskit.Source{{ID: "pipeline", Text: strings.Repeat("a", 70000)}}
	if got := roleSourceBudget(d, "Payments Pulse", input); len(got) != 0 {
		t.Fatal("unrelated pipeline sent to financial role")
	}
	got := roleSourceBudget(d, "Pipeline SDR", input)
	if len(got) != 1 || len(got[0].Text) > 48100 {
		t.Fatal("unbounded role packet")
	}
}

func TestAllTaskRolesRefreshTheirOwnWorkersWithoutDuplicates(t *testing.T) {
	d := &Deps{}
	var metas []agents.Meta
	plan := saleskit.CommandPlan{}
	for _, worker := range AllAgents(d) {
		m := worker.Meta()
		metas = append(metas, m)
		plan.Tasks = append(plan.Tasks, saleskit.CommandTask{Agent: taskRoleName(m), Title: "Work"})
	}
	plan = withTaskWorkers(plan, metas)
	plan = withTaskWorkers(plan, metas)
	if len(plan.Agents) != 35 || len(plan.Tasks) != 35 {
		t.Fatalf("coverage/dedup failed: %+v", plan)
	}
	seats := []string{}
	for _, task := range plan.Tasks {
		seats = append(seats, task.Agent)
	}
	if err := plan.Validate(plan.Agents, seats); err != nil {
		t.Fatal(err)
	}
}

type unavailableRoleWorker struct{}

func (unavailableRoleWorker) Meta() agents.Meta {
	return agents.Meta{ID: "role-worker", Name: "Prospect Research"}
}
func (unavailableRoleWorker) Run(context.Context) (agents.Result, error) {
	return agents.Result{OK: false, Summary: "Optional publishing connection unavailable"}, nil
}

func TestCompletedRoleWorkIsNotBlockedByOptionalEvidenceRefresh(t *testing.T) {
	peopleSetup(t)
	t.Setenv("FOUNDEROS_CRONS", "1")
	oldHandover := taskHandover
	taskHandover = func() (string, error) { return "Owner handover", nil }
	defer func() { taskHandover = oldHandover }()
	b := &commandBoard{done: true}
	oldBoard := askBoardFor
	askBoardFor = func(*Deps) delegator { return b }
	defer func() { askBoardFor = oldBoard }()
	pool := catalogdb.TestDB(t)
	ws := catalogdb.Workspace(t, pool, "evidence-only")
	d := &Deps{Pool: pool, Agents: agents.New(agents.NewMemStore(), unavailableRoleWorker{})}
	p := people.Person{ID: "owner", Email: people.OwnerEmail(), Owner: true}
	c, err := enqueueCommand(context.Background(), d, p, ws, uuid.NewString(), "Prepare an internal report", saleskit.CommandPlan{Tasks: []saleskit.CommandTask{{Agent: "Prospect Research", Title: "Internal report"}}})
	if err != nil {
		t.Fatal(err)
	}
	runCommand(context.Background(), d, &c)
	if c.Status != "complete" || c.Steps[0].Status != "blocked" || !c.Steps[0].EvidenceOnly || c.Steps[1].Status != "complete" {
		t.Fatalf("optional evidence blocked completed task: %+v", c)
	}
}

// The model sees task agents as "Name (status)" and sometimes echoes that, or
// changes the case. Both must resolve to the real seat, and a seat that cannot
// take work must be named in the error instead of "invalid team task".
func TestResolveTaskSeats(t *testing.T) {
	board := []paperclip.Agent{{Name: "Prospect Research", Status: "idle"}, {Name: "Sales Operations", Status: "running"}, {Name: "CEO", Status: "paused"}}
	plan, err := resolveTaskSeats(saleskit.CommandPlan{Tasks: []saleskit.CommandTask{
		{Agent: "Prospect Research (idle)", Title: "US startups"},
		{Agent: " sales operations ", Title: "UK websites"},
	}}, board)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tasks[0].Agent != "Prospect Research" || plan.Tasks[1].Agent != "Sales Operations" {
		t.Fatalf("seats not resolved: %+v", plan.Tasks)
	}
	if _, err := resolveTaskSeats(saleskit.CommandPlan{Tasks: []saleskit.CommandTask{{Agent: "CEO", Title: "x"}}}, board); err == nil || !strings.Contains(err.Error(), "CEO is paused") {
		t.Fatalf("paused seat: %v", err)
	}
	if _, err := resolveTaskSeats(saleskit.CommandPlan{Tasks: []saleskit.CommandTask{{Agent: "Growth Hacker", Title: "x"}}}, board); err == nil || !strings.Contains(err.Error(), `unknown task agent "Growth Hacker"`) {
		t.Fatalf("unknown seat: %v", err)
	}
}
