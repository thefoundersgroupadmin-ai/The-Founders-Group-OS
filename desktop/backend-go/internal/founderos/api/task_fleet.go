package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/agents"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/pages/integrations"
	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
)

var fleetMu sync.Mutex

// These are implemented OS connectors, not every aspirational catalogue tile.
var roleConnections = map[string][]string{
	"reelkit-editor": {"local-stack"}, "renderly-creative": {"local-stack"}, "stack-monitor": {"local-stack"}, "conductor": {"local-stack"},
	"launchpad-cohort-sales": {"trakyo"}, "vantage-sales": {"payments"}, "paykit-sales": {"payments"}, "vantage-paykit": {"payments"},
	"payments-pulse": {"payments"}, "crm-pulse": {"typeform", "calendar", "fathom"},
	"client-roster": {"payments"}, "client-onboarding": {"payments", "slack"}, "client-success": {"fathom", "plaud", "slack"},
	"newsletter-agent": {"beehiiv"}, "comms-digest": {"email", "whatsapp", "slack", "calendar"}, "comms-agent": {"email", "whatsapp", "slack"},
	"gmail-worker": {"email"}, "whatsapp-worker": {"whatsapp"}, "slack-worker": {"slack"},
	"social-agent": {"zernio", "arcads"}, "postly-publisher": {"zernio"}, "adsmith-creative": {"arcads"}, "dmflow-mcp": {"manychat"},
	"data-agent": {"optimal-engine"}, "markdown-auditor": {"optimal-engine"}, "vector-auditor": {"optimal-engine"},
	"brand-deal-agent": {"email", "payments"}, "sales-agent": {"email", "calendar"}, "stripe-sales": {"payments"}, "processor-confirmation": {"payments"}, "sales-calls-data": {"fathom", "plaud"},
}

func taskRoleInstructions(m agents.Meta) string {
	return fmt.Sprintf(`# TFG OS task agent: %s
Worker identity: %s. Department: %s.
Responsibility: %s

You own real tasks for this role, not just connector status checks. Read the assigned work order, workspace profile, source packet, previous-role handoffs and attached workflow methods. Complete the requested outcome within your remit and save a durable task document keyed "report" containing the result, evidence, unknowns, and blockers. Mark the task done only after the document is saved. Do not treat your worker's description or a configured key as proof a service works.

The OS executes the linked worker before your task and attaches recorded worker results. Use those results and authorised task documents; do not invent tool access, browse authenticated owner sessions or read arbitrary Mac paths. When a task genuinely needs missing tools, say which action is blocked and still complete the supported work. Research must cite sources; never guess emails, phones, financial facts or successful actions. Sales drafts use the OS's "outbox" document schema; verified research uses "prospects". Other deliverables belong in task documents with descriptive keys. No external message sending, publishing, payments, credit spending, credential changes or permission changes without an existing OS approval and actual supported execution receipt.

All work runs through TFG OS. Do not redirect the owner to Paperclip or ask them to run individual workflows. Cooperate through the command's previous-role handoff packet; the OS passes your saved report to subsequent task agents. Never create duplicate child tasks, autonomous agent fleets, or background loops. You are activated on demand, not continuously.

There is no usage cutoff or AI pause; work normally. The attached owner handover and work packet satisfy context reads; do not attempt forbidden filesystem helpers when the exact content is supplied.
`, m.Name, m.ID, m.DepartmentID, m.Description)
}

func init() { RegisterPage(registerTaskFleet) }

func registerTaskFleet(s *gin.RouterGroup, d *Deps) {
	s.GET("/pages/agents/fleet", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		if !p.Owner {
			c.JSON(403, gin.H{"error": "Only the installation owner can inspect the task fleet"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		board := paperclip.New(d.Resolver)
		bindings, err := board.TaskRoleBindings(ctx)
		if err != nil {
			c.JSON(503, gin.H{"error": "Task fleet unavailable: " + err.Error()})
			return
		}
		seats, err := board.Agents(ctx)
		if err != nil {
			c.JSON(503, gin.H{"error": "Task agents unavailable: " + err.Error()})
			return
		}
		statuses := d.Board.Statuses(ctx)
		states := map[string]string{}
		for _, v := range statuses {
			states[v.ID] = string(v.State)
		}
		var roles []gin.H
		covered, running, available := 0, 0, 0
		for _, a := range seats {
			if a.Status == "running" {
				running++
			}
			if a.Status == "idle" || a.Status == "running" {
				available++
			}
		}
		if d.Agents != nil {
			for _, m := range d.Agents.List() {
				a, exists := bindings[m.ID]
				if exists {
					covered++
				}
				connections := []gin.H{}
				for _, id := range roleConnections[m.ID] {
					keys := []string{}
					for _, tile := range integrations.Catalog {
						if tile.ConnectorID == id && states[id] != "connected" {
							for _, key := range integrations.ConnectKeysFor(tile) {
								if strings.TrimSpace(d.Resolver.Resolve(key)) == "" {
									keys = append(keys, key)
								}
							}
						}
					}
					state := states[id]
					if state == "" {
						state = "not_checked"
					}
					connections = append(connections, gin.H{"id": id, "state": state, "missingKeys": keys})
				}
				for _, note := range roleOperationLimits[m.ID] {
					connections = append(connections, gin.H{"id": note, "state": "not_implemented", "missingKeys": []string{}})
				}
				roles = append(roles, gin.H{"workerId": m.ID, "name": m.Name, "departmentId": m.DepartmentID, "description": m.Description, "taskAgent": a, "provisioned": exists, "connections": connections})
			}
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, gin.H{"roles": roles, "workers": len(roles), "covered": covered, "taskAgents": len(seats), "available": available, "running": running})
	})
	s.POST("/pages/agents/fleet/provision", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		if !p.Owner {
			c.JSON(403, gin.H{"error": "Only the installation owner can configure task agents"})
			return
		}
		if d.Agents == nil {
			c.JSON(503, gin.H{"error": "Worker registry unavailable"})
			return
		}
		if !fleetMu.TryLock() {
			c.JSON(409, gin.H{"error": "Task fleet setup is already running"})
			return
		}
		defer fleetMu.Unlock()
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
		defer cancel()
		board := paperclip.New(d.Resolver)
		list, err := board.Agents(ctx)
		if err != nil {
			c.JSON(503, gin.H{"error": "Task board unavailable"})
			return
		}
		boss := ""
		for _, a := range list {
			if a.Name == "CEO" {
				boss = a.ID
			}
		}
		rows, rowErr := storeFor(d).Agents(ctx)
		if rowErr != nil {
			c.JSON(503, gin.H{"error": "OS role hierarchy unavailable"})
			return
		}
		parents := map[string]string{}
		for _, row := range rows {
			if row.ParentID != nil {
				parents[row.ID] = *row.ParentID
			}
		}
		ordered, orderErr := orderedTaskRoles(d.Agents.List(), parents)
		if orderErr != nil {
			c.JSON(409, gin.H{"error": orderErr.Error()})
			return
		}
		bindings, bindErr := board.TaskRoleBindings(ctx)
		if bindErr != nil {
			c.JSON(503, gin.H{"error": "Task role bindings unavailable"})
			return
		}
		created, existing := 0, 0
		results := []gin.H{}
		for _, m := range ordered {
			manager := boss
			if parent := parents[m.ID]; parent != "" {
				if lead, ok := bindings[parent]; ok {
					manager = lead.ID
				} else {
					results = append(results, gin.H{"workerId": m.ID, "error": "Parent task role is not provisioned: " + parent})
					continue
				}
			}
			a, new, err := board.EnsureTaskRole(ctx, m.ID, taskRoleName(m), taskRoleInstructions(m), manager)
			if err != nil {
				results = append(results, gin.H{"workerId": m.ID, "error": err.Error()})
				continue
			}
			bindings[m.ID] = a
			if new {
				created++
			} else {
				existing++
			}
			results = append(results, gin.H{"workerId": m.ID, "agentId": a.ID, "created": new})
		}
		c.JSON(200, gin.H{"created": created, "existing": existing, "results": results})
	})
}

// Provision parent roles first, preserving the existing business hierarchy.
func orderedTaskRoles(list []agents.Meta, parents map[string]string) ([]agents.Meta, error) {
	byID := map[string]agents.Meta{}
	for _, m := range list {
		byID[m.ID] = m
	}
	out := []agents.Meta{}
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 2 {
			return nil
		}
		if state[id] == 1 {
			return fmt.Errorf("cyclic task role hierarchy: %s", id)
		}
		m, ok := byID[id]
		if !ok {
			return fmt.Errorf("missing parent worker %s", id)
		}
		state[id] = 1
		if parent := parents[id]; parent != "" {
			if err := visit(parent); err != nil {
				return err
			}
		}
		state[id] = 2
		out = append(out, m)
		return nil
	}
	for _, m := range list {
		if err := visit(m.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// The owner can task the installation's communication roles with real inbox
// previews. Sales workspaces otherwise keep their existing prospect filtering.
func taskRoleSources(ctx context.Context, d *Deps, workspace, role string) []saleskit.Source {
	if workspace != "founderos" && workspace != "personal" {
		return nil
	}
	var worker string
	if d.Agents != nil {
		for _, m := range d.Agents.List() {
			if taskRoleName(m) == role {
				worker = m.ID
			}
		}
	}
	if worker != "gmail-worker" && worker != "whatsapp-worker" && worker != "slack-worker" && worker != "comms-agent" && worker != "comms-digest" && worker != "brand-deal-agent" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	src := commsSourcesFor(d)
	out := []saleskit.Source{}
	remaining := 16000
	add := func(id string, v any, err error) {
		if remaining <= 0 {
			return
		}
		if err != nil {
			out = append(out, saleskit.Source{ID: id, Title: "Communication source unavailable", Text: "Read failed; check OS Connections. Do not invent message content."})
			return
		}
		raw, e := json.Marshal(v)
		if e == nil && string(raw) != "null" {
			out = append(out, saleskit.Source{ID: id, Title: "Owner-authorised communication previews (not complete threads)", Text: clip(string(raw), remaining)})
			remaining -= len(out[len(out)-1].Text)
		}
	}
	if worker == "gmail-worker" || worker == "comms-agent" || worker == "comms-digest" || worker == "brand-deal-agent" {
		if src.Emails != nil {
			v, e := src.Emails(ctx)
			if len(v) > 40 {
				v = v[:40]
			}
			add("role-email-previews", v, e)
		}
	}
	if worker == "slack-worker" || worker == "comms-agent" || worker == "comms-digest" {
		if src.SlackMessages != nil {
			v, e := src.SlackMessages(ctx, 40)
			add("role-slack-previews", v, e)
		}
	}
	if worker == "whatsapp-worker" || worker == "comms-agent" || worker == "comms-digest" {
		if src.WhatsApp != nil {
			v, _ := src.WhatsApp(ctx)
			if len(v) > 40 {
				v = v[:40]
			}
			add("role-whatsapp-previews", v, nil)
		}
	}
	return out
}

// Keep the dedicated cockpit responder distinct from its task operations role.
func taskRoleName(m agents.Meta) string {
	if m.ID == "conductor" {
		return "Conductor Operations"
	}
	return m.Name
}

func roleSourceBudget(d *Deps, role string, sources []saleskit.Source) []saleskit.Source {
	department := ""
	if d.Agents != nil {
		for _, m := range d.Agents.List() {
			if taskRoleName(m) == role {
				department = m.DepartmentID
			}
		}
	}
	out := []saleskit.Source{}
	remaining := 48000
	for _, source := range sources {
		// Business tasks use pipeline evidence. Technical/financial/creative tasks
		// use their linked worker's real data and any prior task handoffs instead.
		if department != "" && department != "dept-sales" && department != "dept-clients" {
			continue
		}
		if remaining <= 0 {
			break
		}
		if len(source.Text) > remaining {
			source.Text = clip(source.Text, remaining)
		}
		remaining -= len(source.Text)
		out = append(out, source)
	}
	return out
}

// Matching role work always refreshes the associated worker exactly once.
func withTaskWorkers(plan saleskit.CommandPlan, workers []agents.Meta) saleskit.CommandPlan {
	seen := map[string]bool{}
	for _, id := range plan.Agents {
		seen[id] = true
	}
	for _, task := range plan.Tasks {
		for _, worker := range workers {
			if taskRoleName(worker) == task.Agent && !seen[worker.ID] {
				plan.Agents = append(plan.Agents, worker.ID)
				seen[worker.ID] = true
			}
		}
	}
	return plan
}

// Preserve action-level gaps even after a credential is supplied. These legacy
// workers must not appear fully functional merely because a key exists.
var roleOperationLimits = map[string][]string{
	"paykit-sales":      {"Legacy worker alias only checks PAYKIT_API_KEY; use verified Payments reads for financial evidence"},
	"vantage-paykit":    {"Legacy worker alias only checks PAYKIT_API_KEY; use verified Payments reads for financial evidence"},
	"reelkit-editor":    {"Render adapter requires REMOTION_RENDER_URL and REMOTION_RENDER_TOKEN; compatible render service and live validation required"},
	"renderly-creative": {"Generation adapter requires HIGGSFIELD_API_KEY and live provider validation"},
}
