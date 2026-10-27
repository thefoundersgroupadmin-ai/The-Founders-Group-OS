package paperclip

import (
	"context"
	"net/url"
	"sort"
	"strings"
)

type ToolAssignment struct {
	Tools []string `json:"tools"`
	State string   `json:"toolState"`
}

// OSActionAssignments reads saved profiles and installs, not credentials or
// inferred role defaults. This is a display projection, never an execution grant.
func (c *Connector) OSActionAssignments(ctx context.Context) (map[string]ToolAssignment, error) {
	out := map[string]ToolAssignment{}
	body, err := c.companyGet(ctx, "/tools/connections")
	if err != nil {
		return nil, err
	}
	var connection map[string]any
	for _, v := range listFrom(body, "connections") {
		m := obj(v)
		if m["name"] == "TFG OS task tools" {
			connection = m
			break
		}
	}
	if connection == nil {
		return out, nil
	}
	cid := stringValue(connection["id"])
	base := "/api/tool-connections/" + url.PathEscape(cid)
	body, err = c.get(ctx, base+"/catalog", issueTimeout)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, v := range listFrom(body, "catalog") {
		m := obj(v)
		name := stringValue(m["toolName"])
		if name == "" {
			name = stringValue(m["name"])
		}
		if name != "" {
			names[stringValue(m["id"])] = name
		}
	}
	body, err = c.get(ctx, base+"/installs", issueTimeout)
	if err != nil {
		return nil, err
	}
	installed := map[string]bool{}
	companyInstall := false
	for _, v := range listFrom(body, "installs") {
		m := obj(v)
		if m["targetType"] == "agent" {
			installed[stringValue(m["targetId"])] = true
		}
		if m["targetType"] == "company" {
			companyInstall = true
		}
	}
	body, err = c.companyGet(ctx, "/tools/profiles")
	if err != nil {
		return nil, err
	}
	for _, v := range listFrom(body, "profiles") {
		p := obj(v)
		if !strings.HasPrefix(stringValue(p["profileKey"]), "tfg-os-") || p["status"] != "active" {
			continue
		}
		include, exclude := map[string]bool{}, map[string]bool{}
		for _, entry := range listFrom(p, "entries") {
			e := obj(entry)
			if e["connectionId"] != cid {
				continue
			}
			name := names[stringValue(e["catalogEntryId"])]
			if name == "" {
				continue
			}
			if e["effect"] == "include" {
				include[name] = true
			} else if e["effect"] == "exclude" {
				exclude[name] = true
			}
		}
		tools := []string{}
		for name := range include {
			if !exclude[name] {
				tools = append(tools, name)
			}
		}
		sort.Strings(tools)
		for _, binding := range listFrom(p, "bindings") {
			b := obj(binding)
			if b["targetType"] != "agent" {
				continue
			}
			id := stringValue(b["targetId"])
			state := "assigned"
			if !installed[id] && !companyInstall {
				state = "not_installed"
			} else if connection["status"] != "active" || connection["enabled"] != true {
				state = "disabled"
			} else if len(tools) == 0 {
				state = "not_assigned"
			}
			out[id] = ToolAssignment{Tools: tools, State: state}
		}
	}
	return out, nil
}
