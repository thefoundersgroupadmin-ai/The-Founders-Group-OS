package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
	"github.com/rhl/businessos-backend/internal/founderos/toolbridge"
)

func verifyNativeToolRun(ctx context.Context, d *Deps, a toolActor) error {
	return paperclip.New(d.Resolver).VerifyToolRun(ctx, a.RunID, a.AgentID, a.TaskID)
}
func toolActorForApproval(ctx context.Context, d *Deps, commandID, taskID, agentID, runID string) (toolActor, error) {
	h := http.Header{}
	h.Set("X-Paperclip-Company-Id", d.Resolver.Resolve("PAPERCLIP_COMPANY_ID"))
	h.Set("X-Paperclip-Issue-Id", taskID)
	h.Set("X-Paperclip-Agent-Id", agentID)
	h.Set("X-Paperclip-Run-Id", runID)
	a, err := loadToolActor(ctx, d, h, false)
	if err == nil && a.Command.ID != commandID {
		return a, errors.New("approval command mismatch")
	}
	return a, err
}
func returnToolReceipt(ctx context.Context, d *Deps, a toolActor, id string, value any) error {
	raw, _ := json.MarshalIndent(value, "", "  ")
	if err := paperclip.New(d.Resolver).ReturnToolReceipt(ctx, a.TaskID, a.AgentID, id, string(raw)); err != nil {
		return err
	}
	// Owner approval authorises this exact continuation; completed steps remain
	// durable and the external operation is not replayed.
	_, err := d.Pool.Exec(ctx, `UPDATE founderos_commands c SET status='waiting',steps=(SELECT jsonb_agg(CASE WHEN s->>'taskId'=$2 THEN s||'{"status":"waiting"}'::jsonb ELSE s END ORDER BY ord) FROM jsonb_array_elements(c.steps) WITH ORDINALITY v(s,ord)),updated_at=now() WHERE id=$1 AND status IN ('blocked','waiting')`, a.Command.ID, a.TaskID)
	return err
}

var providerJobID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}$`)
var providerModel = regexp.MustCompile(`^[A-Za-z0-9_/-]{1,180}$`)

func executeProviderTool(ctx context.Context, d *Deps, a toolActor, t toolbridge.Tool, input json.RawMessage) (any, error) {
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return nil, errors.New("invalid provider input")
	}
	target, header, credential := "", "", ""
	payload := input
	switch t.Provider {
	case "lusha":
		contacts, ok := args["contacts"].([]any)
		if !ok || len(contacts) == 0 || len(contacts) > 10 {
			return nil, errors.New("provide 1–10 contacts using Lusha v3 contacts identifiers; bounded reveal only")
		}
		target = "https://api.lusha.com" + t.Path
		header = "api_key"
		credential = d.Resolver.Resolve("LUSHA_API_KEY")
	case "higgsfield", "remotion":
		base := "https://api.higgsfield.ai"
		header = "Authorization"
		credential = "Key " + d.Resolver.Resolve("HIGGSFIELD_API_KEY")
		if t.Provider == "remotion" {
			base = strings.TrimRight(d.Resolver.Resolve("REMOTION_RENDER_URL"), "/")
			credential = "Bearer " + d.Resolver.Resolve("REMOTION_RENDER_TOKEN")
		}
		if strings.HasSuffix(t.Name, "_generate") {
			model, _ := args["model"].(string)
			if !providerModel.MatchString(model) || strings.Contains(model, "..") {
				return nil, errors.New("a valid model path is required")
			}
			target = base + "/" + model
			body, ok := args["parameters"].(map[string]any)
			if !ok {
				return nil, errors.New("parameters must match the selected provider model schema")
			}
			payload, _ = json.Marshal(body)
		} else if strings.HasSuffix(t.Name, "_render") {
			target = base + "/render"
		} else {
			id, _ := args["requestId"].(string)
			if !providerJobID.MatchString(id) {
				return nil, errors.New("a valid requestId is required")
			}
			var owned bool
			err := d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM founderos_tool_calls WHERE task_id=$1 AND status='succeeded' AND tool=$2 AND (output->>'request_id'=$3 OR output->>'id'=$3))`, a.TaskID, t.Provider+map[string]string{"higgsfield": "_generate", "remotion": "_render"}[t.Provider], id).Scan(&owned)
			if err != nil || !owned {
				return nil, errors.New("job was not created by this task")
			}
			if t.Provider == "higgsfield" {
				target = base + "/requests/" + id + "/status"
				if strings.HasSuffix(t.Name, "_cancel") {
					target = base + "/requests/" + id + "/cancel"
				}
			} else {
				target = base + "/jobs/" + id
				if strings.HasSuffix(t.Name, "_cancel") {
					target += "/cancel"
				}
			}
		}

	default:
		return nil, errors.New("provider adapter unavailable")
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(t.Provider == "remotion" && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return nil, errors.New("provider endpoint must be owner-configured HTTPS (loopback render service may use HTTP)")
	}
	var value any
	send := func() error {
		body := bytes.NewReader(payload)
		if t.Method == "GET" {
			body = bytes.NewReader(nil)
		}
		req, err := http.NewRequestWithContext(ctx, t.Method, target, body)
		if err != nil {
			return err
		}
		req.Header.Set(header, credential)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := client.Do(req)
		if err != nil {
			return errors.New("provider response unavailable; outcome may be uncertain")
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, 512001))
		if err != nil || len(raw) > 512000 {
			return errors.New("provider response exceeded limits")
		}
		if res.StatusCode >= 300 {
			return fmt.Errorf("provider returned HTTP %d; no automatic retry", res.StatusCode)
		}
		if json.Unmarshal(raw, &value) != nil {
			return errors.New("provider did not return a JSON receipt")
		}
		return nil
	}
	if t.Approval {
		err = guard.Outbound("agent-tool."+t.Name, send)
	} else {
		err = send()
	}
	return value, err
}

func updateToolContact(ctx context.Context, d *Deps, a toolActor, input json.RawMessage) (any, error) {
	var b struct {
		ContactID  string  `json:"contactId"`
		NextAction *string `json:"nextAction"`
		BuyerRole  *string `json:"buyerRole"`
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if dec.Decode(&b) != nil || b.ContactID == "" || (b.NextAction == nil && b.BuyerRole == nil) {
		return nil, errors.New("only contactId, nextAction and buyerRole are accepted")
	}
	// Writes stay in the command's selected workspace; source bindings grant read
	// access, not permission to modify a different company's records.
	tag, err := d.Pool.Exec(ctx, `UPDATE founderos_funnel_contacts SET next_action=COALESCE($3,next_action),buyer_role=COALESCE($4,buyer_role) WHERE id=$1 AND workspace_id=$2::uuid`, b.ContactID, a.Command.WorkspaceID, b.NextAction, b.BuyerRole)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, errors.New("contact is not in the writable command workspace")
	}
	return map[string]any{"updated": true, "contactId": b.ContactID}, nil
}
