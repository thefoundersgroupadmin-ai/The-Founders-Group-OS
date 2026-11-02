package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rhl/businessos-backend/internal/founderos/memory"
	"github.com/rhl/businessos-backend/internal/founderos/people"
	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
	"github.com/rhl/businessos-backend/internal/founderos/toolbridge"
)

type toolActor struct {
	Command                                  saleskit.Command
	Config                                   saleskit.AutomationConfig
	TaskID, AgentID, RunID, Role, Department string
}
type toolCall struct {
	ID     string          `json:"id"`
	Tool   string          `json:"tool"`
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
	TaskID string          `json:"taskId"`
}

var toolRouters sync.Map
var toolApprovalMu sync.Mutex

func init() { RegisterPage(registerAgentTools) }

var agentToolKey = toolBridgeKey

func toolBridgeKey() (string, error) {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".founderos", "agent-tools.key")
	if b, err := os.ReadFile(p); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		b, e := os.ReadFile(p)
		return strings.TrimSpace(string(b)), e
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(key)
	return key, err
}

// Only the authenticated, server-owned Paperclip gateway supplies these headers.
// An agent's input never selects its identity or an arbitrary workspace.
func toolActorFor(ctx context.Context, d *Deps, h http.Header) (toolActor, error) {
	return loadToolActor(ctx, d, h, true)
}
func loadToolActor(ctx context.Context, d *Deps, h http.Header, verify bool) (toolActor, error) {
	var a toolActor
	a.TaskID = h.Get("X-Paperclip-Issue-Id")
	a.AgentID = h.Get("X-Paperclip-Agent-Id")
	a.RunID = h.Get("X-Paperclip-Run-Id")
	if h.Get("X-Paperclip-Company-Id") != d.Resolver.Resolve("PAPERCLIP_COMPANY_ID") {
		return a, errors.New("company context mismatch")
	}
	for _, v := range []string{a.TaskID, a.AgentID, a.RunID} {
		if _, err := uuid.Parse(v); err != nil {
			return a, errors.New("verified task, agent and run context required")
		}
	}
	var raw, steps []byte
	err := d.Pool.QueryRow(ctx, `SELECT id::text,workspace_id::text,user_id,user_email,user_name,request,plan,steps,status FROM founderos_commands WHERE steps @> $1::jsonb ORDER BY created_at DESC LIMIT 1`, fmt.Sprintf(`[{"taskId":%q}]`, a.TaskID)).Scan(&a.Command.ID, &a.Command.WorkspaceID, &a.Command.UserID, &a.Command.UserEmail, &a.Command.UserName, &a.Command.Request, &raw, &steps, &a.Command.Status)
	if err != nil {
		return a, errors.New("task is not an OS work order")
	}
	json.Unmarshal(raw, &a.Command.Plan)
	json.Unmarshal(steps, &a.Command.Steps)
	if a.Command.Status == "interrupted" || a.Command.Status == "complete" {
		return a, errors.New("command stopped")
	}
	p := people.Person{ID: a.Command.UserID, Email: a.Command.UserEmail, Name: a.Command.UserName, Owner: people.IsOwner(a.Command.UserEmail)}
	if !p.Owner {
		return a, errors.New("owner authorisation revoked")
	}
	cfg, err := commandConfig(ctx, d, p, a.Command.WorkspaceID)
	if err != nil {
		return a, err
	}
	a.Config = cfg
	for _, s := range a.Command.Steps {
		if s.Kind == "task" && s.TaskID == a.TaskID {
			index := mustTaskIndex(s.ID)
			if index < 0 || index >= len(a.Command.Plan.Tasks) {
				return a, errors.New("invalid task binding")
			}
			a.Role = a.Command.Plan.Tasks[index].Agent
		}
	}
	if a.Role == "" {
		return a, errors.New("task role unavailable")
	}
	if d.Agents != nil {
		for _, m := range d.Agents.List() {
			if taskRoleName(m) == a.Role {
				a.Department = m.DepartmentID
			}
		}
	}
	if verify {
		if err := verifyNativeToolRun(ctx, d, a); err != nil {
			return a, err
		}
	}
	return a, nil
}

func agentToolMCP(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, err := agentToolKey()
		got := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || err != nil || len(got) != len(key) || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			c.JSON(401, gin.H{"error": "invalid tool gateway credential"})
			return
		}
		var req struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
			Params  struct {
				Name      string `json:"name"`
				Arguments struct {
					RequestID string          `json:"requestId"`
					Input     json.RawMessage `json:"input"`
				} `json:"arguments"`
			} `json:"params"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128000)
		if c.ShouldBindJSON(&req) != nil {
			c.JSON(400, gin.H{"error": "invalid tool request"})
			return
		}
		result := any(gin.H{})
		var callErr error
		switch req.Method {
		case "initialize":
			result = gin.H{"protocolVersion": "2024-11-05", "capabilities": gin.H{"tools": gin.H{}}, "serverInfo": gin.H{"name": "TFG OS task tools", "version": "1.0.0"}}
		case "notifications/initialized":
			c.Status(202)
			return
		case "ping":
		case "tools/list":
			rows := []gin.H{}
			for _, t := range toolbridge.Catalog {
				rows = append(rows, gin.H{"name": t.Name, "description": t.Description + toolInputHelp(t.Name), "inputSchema": gin.H{"type": "object", "additionalProperties": false, "required": []string{"requestId", "input"}, "properties": gin.H{"requestId": gin.H{"type": "string", "format": "uuid"}, "input": gin.H{"type": "object"}}}, "annotations": gin.H{"readOnlyHint": !t.Approval, "destructiveHint": t.Approval}})
			}
			roles := map[string][]string{}
			if d.Agents != nil {
				for _, m := range d.Agents.List() {
					name := taskRoleName(m)
					for _, tool := range toolbridge.Catalog {
						if toolbridge.Allowed(name, m.DepartmentID, tool) {
							roles[name] = append(roles[name], tool.Name)
						}
					}
				}
			}
			for _, name := range []string{"CEO", "Prospect Research", "Sales Operations", "Sales Enablement and Delivery"} {
				for _, tool := range toolbridge.Catalog {
					if toolbridge.Allowed(name, "", tool) {
						roles[name] = append(roles[name], tool.Name)
					}
				}
			}
			result = gin.H{"tools": rows, "_meta": gin.H{"roleTools": roles}}
		case "tools/call":
			var actor toolActor
			actor, callErr = toolActorFor(c.Request.Context(), d, c.Request.Header)
			if callErr == nil {
				var v any
				v, callErr = invokeAgentTool(c.Request.Context(), d, actor, req.Params.Name, req.Params.Arguments.RequestID, req.Params.Arguments.Input)
				if callErr == nil {
					raw, _ := json.Marshal(v)
					result = gin.H{"content": []gin.H{{"type": "text", "text": string(raw)}}}
				}
			}
		default:
			callErr = errors.New("unsupported MCP method")
		}
		if callErr != nil {
			c.JSON(200, gin.H{"jsonrpc": "2.0", "id": req.ID, "result": gin.H{"isError": true, "content": []gin.H{{"type": "text", "text": callErr.Error()}}}})
			return
		}
		c.JSON(200, gin.H{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

func invokeAgentTool(ctx context.Context, d *Deps, a toolActor, name, requestID string, input json.RawMessage) (any, error) {
	t, ok := toolbridge.Find(name)
	if !ok || !toolbridge.Allowed(a.Role, a.Department, t) {
		return nil, errors.New("tool is not assigned to this role")
	}
	if t.Global && a.Config.Workspace != "founderos" && a.Config.Workspace != "personal" {
		return nil, errors.New("installation-wide tool requires an HQ/Personal work order")
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return nil, errors.New("requestId must be a new UUID, reused for this exact intended action")
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	var obj map[string]any
	if json.Unmarshal(input, &obj) != nil || obj == nil {
		return nil, errors.New("input must be an object")
	}
	if hasToolCredentials(obj) {
		return nil, errors.New("credentials must stay in OS Connections, never tool arguments")
	}
	input, _ = json.Marshal(obj)
	id := uuid.NewString()
	state := "running"
	if t.Approval {
		state = "needs_approval"
	}
	missing := []string{}
	for _, key := range t.Keys {
		if d.Resolver.Resolve(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return gin.H{"status": "missing_credentials", "requiredKeys": missing, "tool": name, "pathway": "implemented; live provider validation still required"}, nil
	}
	tag, err := d.Pool.Exec(ctx, `INSERT INTO founderos_tool_calls(id,command_id,task_id,agent_id,run_id,request_id,tool,input,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(task_id,request_id) DO NOTHING`, id, a.Command.ID, a.TaskID, a.AgentID, a.RunID, requestID, name, input, state)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		var prior toolCall
		var equal bool
		err = d.Pool.QueryRow(ctx, `SELECT id::text,tool,status,input,output,task_id,input=$3::jsonb FROM founderos_tool_calls WHERE task_id=$1 AND request_id=$2`, a.TaskID, requestID, input).Scan(&prior.ID, &prior.Tool, &prior.Status, &prior.Input, &prior.Output, &prior.TaskID, &equal)
		if err != nil {
			return nil, err
		}
		if !equal || prior.Tool != name {
			return nil, errors.New("requestId already belongs to a different action")
		}
		return prior, nil
	}
	if t.Approval {
		return gin.H{"id": id, "status": state, "reviewUrl": "/os/approvals", "message": "Exact action and input await owner review. Do not repeat with a new requestId. Wait for the receipt."}, nil
	}
	return settleAgentTool(ctx, d, a, id, t, input)
}

func settleAgentTool(ctx context.Context, d *Deps, a toolActor, id string, t toolbridge.Tool, input json.RawMessage) (any, error) {
	value, err := executeAgentTool(ctx, d, a, t, input)
	state := "succeeded"
	if err != nil {
		state = "failed"
		if t.Approval {
			state = "uncertain"
		}
		value = gin.H{"error": err.Error(), "message": "No automatic retry. Review provider state before any new action."}
	}
	raw, _ := json.Marshal(value)
	if len(raw) > 512000 {
		raw, _ = json.Marshal(gin.H{"error": "result exceeds tool response limit", "status": "review_required"})
		state = "uncertain"
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, e := d.Pool.Exec(pctx, `UPDATE founderos_tool_calls SET status=$2,output=$3,updated_at=now() WHERE id=$1`, id, state, raw); e != nil {
		return nil, e
	}
	return gin.H{"id": id, "status": state, "output": json.RawMessage(raw)}, nil
}

// Reuse the OS handlers under a private, in-process router. The gateway has
// already authenticated a real run and owner work order. Only catalogue paths
// can reach this router; callers never select a method, path, user or secret.
func executeAgentTool(ctx context.Context, d *Deps, a toolActor, t toolbridge.Tool, input json.RawMessage) (any, error) {
	ctx = people.With(ctx, people.Person{ID: a.Command.UserID, Email: a.Command.UserEmail, Name: a.Command.UserName, Owner: true})
	if t.Provider != "" {
		return executeProviderTool(ctx, d, a, t, input)
	}
	if t.Name == "knowledge_query" || t.Name == "knowledge_capture" {
		router := RosterDeps(d).Memory
		if router == nil {
			return nil, errors.New("knowledge engine is unavailable")
		}
		var args map[string]any
		json.Unmarshal(input, &args)
		if t.Name == "knowledge_query" {
			q, _ := args["q"].(string)
			if strings.TrimSpace(q) == "" {
				return nil, errors.New("q is required")
			}
			return router.Retrieve(ctx, q, []string{a.Config.Workspace}, memory.RetrieveOptions{}), nil
		}
		title, _ := args["title"].(string)
		text, _ := args["text"].(string)
		if strings.TrimSpace(title) == "" || strings.TrimSpace(text) == "" || len(text) > 30000 {
			return nil, errors.New("title and bounded text are required")
		}
		id, err := router.Capture(ctx, memory.Capture{Workspace: a.Config.Workspace, Title: title, Text: text, Genre: "note", Node: "task-" + uuid.NewString()})
		return gin.H{"id": id, "workspace": a.Config.Workspace}, err
	}
	if t.Name == "pipeline_read" {
		return connectedSalesSnapshot(ctx, d, a.Config)
	}
	if t.Name == "pipeline_update" {
		return updateToolContact(ctx, d, a, input)
	}
	p := people.Person{ID: a.Command.UserID, Email: a.Command.UserEmail, Name: a.Command.UserName, Owner: true}
	var r *gin.Engine
	if value, ok := toolRouters.Load(d); ok {
		r = value.(*gin.Engine)
	} else {
		r = gin.New()
		g := r.Group("/api/founderos")
		mountPages(g, d)
		value, _ := toolRouters.LoadOrStore(d, r)
		r = value.(*gin.Engine)
	}
	var args map[string]any
	json.Unmarshal(input, &args)
	args["workspaceId"] = a.Command.WorkspaceID
	path := "/api/founderos" + t.Path
	body, _ := json.Marshal(args)
	if t.Method == "GET" {
		query := url.Values{}
		for k, v := range args {
			switch v.(type) {
			case string, float64, bool:
				query.Set(k, fmt.Sprint(v))
			}
		}
		path += "?" + query.Encode()
		body = nil
	}
	req, _ := http.NewRequestWithContext(people.With(ctx, p), t.Method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := &toolResponse{header: http.Header{}, code: 200}
	r.ServeHTTP(w, req)
	if w.code >= 300 {
		return nil, fmt.Errorf("OS action returned HTTP %d: %s", w.code, clip(w.body.String(), 500))
	}
	var value any
	if json.Unmarshal(w.body.Bytes(), &value) != nil {
		return nil, errors.New("OS action returned a non-JSON receipt")
	}
	return value, nil
}

type toolResponse struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (w *toolResponse) Header() http.Header { return w.header }
func (w *toolResponse) WriteHeader(n int)   { w.code = n }
func (w *toolResponse) Write(b []byte) (int, error) {
	if w.body.Len()+len(b) > 1024000 {
		return 0, errors.New("tool result too large")
	}
	return w.body.Write(b)
}

func registerAgentTools(s *gin.RouterGroup, d *Deps) {
	s.GET("/pages/agent-tools", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		if !p.Owner {
			c.Status(403)
			return
		}
		rows, err := d.Pool.Query(c.Request.Context(), `SELECT t.id::text,t.tool,t.status,t.input,t.output,t.task_id FROM founderos_tool_calls t JOIN founderos_commands c ON c.id=t.command_id WHERE c.user_id=$1 ORDER BY t.created_at DESC LIMIT 100`, p.ID)
		if err != nil {
			dataErr(c, err)
			return
		}
		defer rows.Close()
		out := []toolCall{}
		for rows.Next() {
			var v toolCall
			if rows.Scan(&v.ID, &v.Tool, &v.Status, &v.Input, &v.Output, &v.TaskID) == nil {
				out = append(out, v)
			}
		}
		c.JSON(200, gin.H{"requests": out, "tools": toolbridge.Catalog})
	})
	s.POST("/pages/agent-tools/:id/:decision", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		if !p.Owner {
			c.Status(403)
			return
		}
		if !toolApprovalMu.TryLock() {
			c.JSON(409, gin.H{"error": "An action decision is already being applied"})
			return
		}
		defer toolApprovalMu.Unlock()
		var v toolCall
		var commandID, agentID, runID string
		err := d.Pool.QueryRow(c.Request.Context(), `SELECT t.id::text,t.tool,t.status,t.input,t.output,t.task_id,t.command_id::text,t.agent_id,t.run_id FROM founderos_tool_calls t JOIN founderos_commands c ON c.id=t.command_id WHERE t.id=$1::uuid AND c.user_id=$2`, c.Param("id"), p.ID).Scan(&v.ID, &v.Tool, &v.Status, &v.Input, &v.Output, &v.TaskID, &commandID, &agentID, &runID)
		if err != nil {
			c.JSON(404, gin.H{"error": "Action not found"})
			return
		}
		if v.Status != "needs_approval" {
			c.JSON(409, gin.H{"error": "Action already decided; inspect its receipt"})
			return
		}
		if c.Param("decision") == "reject" {
			result := gin.H{"id": v.ID, "status": "rejected", "message": "Owner rejected this action. Do not execute or substitute another action."}
			raw, _ := json.Marshal(result)
			_, err = d.Pool.Exec(c.Request.Context(), `UPDATE founderos_tool_calls SET status='rejected',output=$2,updated_at=now() WHERE id=$1 AND status='needs_approval'`, v.ID, raw)
			if err != nil {
				dataErr(c, err)
				return
			}
			a, actorErr := toolActorForApproval(c.Request.Context(), d, commandID, v.TaskID, agentID, runID)
			if actorErr != nil {
				result["deliveryError"] = actorErr.Error()
			} else if deliveryErr := returnToolReceipt(c.Request.Context(), d, a, v.ID, result); deliveryErr != nil {
				result["deliveryError"] = deliveryErr.Error()
			}
			c.JSON(200, result)
			return
		}
		if c.Param("decision") != "approve" {
			c.Status(400)
			return
		}
		a, err := toolActorForApproval(c.Request.Context(), d, commandID, v.TaskID, agentID, runID)
		if err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		t, ok := toolbridge.Find(v.Tool)
		if !ok || !toolbridge.Allowed(a.Role, a.Department, t) {
			c.Status(403)
			return
		}
		for _, key := range t.Keys {
			if d.Resolver.Resolve(key) == "" {
				c.JSON(409, gin.H{"error": "Required credential is missing: " + key})
				return
			}
		}
		claim, claimErr := d.Pool.Exec(c.Request.Context(), `UPDATE founderos_tool_calls SET status='running',updated_at=now() WHERE id=$1 AND status='needs_approval'`, v.ID)
		if claimErr != nil || claim.RowsAffected() != 1 {
			c.JSON(409, gin.H{"error": "Action has already been claimed"})
			return
		}
		result, err := settleAgentTool(c.Request.Context(), d, a, v.ID, t, v.Input)
		if err != nil {
			dataErr(c, err)
			return
		}
		if deliveryErr := returnToolReceipt(c.Request.Context(), d, a, v.ID, result); deliveryErr != nil {
			c.JSON(200, gin.H{"receipt": result, "deliveryError": deliveryErr.Error(), "message": "Action outcome saved; task notification failed. Do not execute the action again."})
			return
		}
		c.JSON(200, result)
	})
}

func recoverAgentTools(d *Deps) {
	if d.Pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = d.Pool.Exec(ctx, `UPDATE founderos_tool_calls SET status='uncertain',output='{"error":"OS restarted during execution; inspect provider state before an explicit new action"}'::jsonb,updated_at=now() WHERE status='running'`)
}

func hasToolCredentials(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			k := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if k == "apikey" || k == "authorization" || k == "password" || k == "secret" || k == "accesstoken" || k == "refreshtoken" || k == "privatekey" {
				return true
			}
			if hasToolCredentials(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasToolCredentials(item) {
				return true
			}
		}
	}
	return false
}

func toolInputHelp(name string) string {
	examples := map[string]string{
		"clients_work":      `{"action":"save","clientId":"verified client roster ID","brief":"Exact reviewed work brief"}`,
		"content_create":    `{"name":"Reviewed lead magnet name","url":"verified published URL"}`,
		"recordings_import": `{}`,
		"apollo_enrich":     `{"limit":1}`,
		"dialer_dnc":        `{"number":"verified E.164 number","reason":"Reviewed reason"}`,
		"contacts_tag":      `{"person":"verified person identifier","channel":"verified channel","tag":"Reviewed tag","tier":1}`,
		"attribution_link":  `{"url":"verified destination URL","content_item_id":"verified ci_ identifier","append_tracking_param":false}`,

		"pipeline_update": `{"contactId":"existing scoped contact ID","nextAction":"verified next action","buyerRole":"verified role"}`,
		"knowledge_query": `{"q":"specific question"}`, "knowledge_capture": `{"title":"Note title","text":"Sourced note text"}`,
		"comms_reply":         `{"source":"email or slack","channel":"Slack channel if used","account":"known inbox account","to":"verified email recipient","subject":"Subject","text":"Exact approved message"}`,
		"social_publish":      `{"caption":"Exact approved copy","platforms":["linkedin"],"mediaUrls":[]}`,
		"social_dm":           `{"subscriberId":"verified subscriber ID","text":"Exact approved message"}`,
		"lusha_enrich":        `{"contacts":[{"clientReferenceId":"OS contact ID","firstName":"Verified first name","lastName":"Verified last name","companyDomain":"verified.example"}],"reveal":["emails","phones"],"options":{"waterfallEnabled":false}}`,
		"higgsfield_generate": `{"model":"model/path from the provider catalogue","parameters":{"prompt":"Reviewed prompt","aspect_ratio":"16:9"}}`,
		"higgsfield_status":   `{"requestId":"request_id from this task's generation receipt"}`, "higgsfield_cancel": `{"requestId":"request_id from this task's generation receipt"}`,
		"remotion_render": `{"compositionId":"owner-configured composition","inputProps":{}}`, "remotion_status": `{"requestId":"job ID from this task's render receipt"}`, "remotion_cancel": `{"requestId":"job ID from this task's render receipt"}`,
	}
	if example, ok := examples[name]; ok {
		return " Input example (replace placeholders with verified values): " + example
	}
	return " Use an empty input object for a read with no filters. Existing OS action validators reject incomplete mutations; never invent required values."
}
