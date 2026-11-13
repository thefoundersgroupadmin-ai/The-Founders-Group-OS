// Package toolbridge defines the closed, server-owned task tool surface.
package toolbridge

import "strings"

type Tool struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Department  string   `json:"department"`
	Method      string   `json:"method"`
	Path        string   `json:"path,omitempty"`
	Keys        []string `json:"requiredKeys,omitempty"`
	Approval    bool     `json:"requiresApproval"`
	Global      bool     `json:"global,omitempty"`
	Provider    string   `json:"provider,omitempty"`
}

var Catalog = []Tool{
	{Name: "pipeline_read", Description: "Read the authorised workspace pipeline and evidence", Department: "sales"},
	{Name: "pipeline_update", Description: "Update an existing scoped contact after owner review", Department: "sales", Approval: true},
	{Name: "knowledge_query", Description: "Query OS knowledge", Department: "tech", Method: "GET", Path: "/pages/brain/query"},
	{Name: "knowledge_capture", Description: "Capture a knowledge note after review", Department: "tech", Method: "POST", Path: "/pages/brain/dump", Approval: true},
	{Name: "comms_read", Description: "Read observed inbox previews (HQ/Personal only)", Department: "comms", Method: "GET", Path: "/pages/comms", Global: true},
	{Name: "comms_reply", Description: "Send an exact owner-approved email or Slack reply", Department: "comms", Method: "POST", Path: "/pages/comms/reply", Approval: true, Global: true},
	{Name: "clients_read", Description: "Read client roster and servicing evidence", Department: "clients", Method: "GET", Path: "/pages/clients", Global: true},
	{Name: "clients_work", Description: "Create a client work item after review", Department: "clients", Method: "POST", Path: "/pages/clients/work", Approval: true, Global: true},
	{Name: "social_read", Description: "Read connected social accounts and posts", Department: "marketing-growth", Method: "GET", Path: "/pages/social", Global: true},
	{Name: "social_publish", Description: "Publish exact reviewed content through Zernio", Department: "marketing-growth", Method: "POST", Path: "/pages/social/posts", Keys: []string{"ZERNIO_API_KEY"}, Approval: true, Global: true},
	{Name: "social_dm", Description: "Send exact reviewed ManyChat message", Department: "marketing-growth", Method: "POST", Path: "/pages/social/dm/reply", Keys: []string{"MANYCHAT_API_KEY"}, Approval: true, Global: true},
	{Name: "newsletter_read", Description: "Read Beehiiv publication and campaign evidence", Department: "marketing-growth", Method: "GET", Path: "/pages/social/beehiiv", Keys: []string{"BEEHIIV_API_KEY", "BEEHIIV_PUBLICATION_ID"}, Global: true},
	{Name: "content_read", Description: "Read OS content inventory", Department: "marketing-growth", Method: "GET", Path: "/pages/content", Global: true},
	{Name: "content_create", Description: "Create a reviewed lead magnet", Department: "marketing-growth", Method: "POST", Path: "/pages/content/lead-magnets", Approval: true, Global: true},
	{Name: "recordings_read", Description: "Read stored call recordings", Department: "sales", Method: "GET", Path: "/pages/calls/archive", Global: true},
	{Name: "recordings_import", Description: "Import a reviewed recording", Department: "sales", Method: "POST", Path: "/pages/plaud/ingest", Approval: true, Global: true},
	{Name: "finance_read", Description: "Read payment processor and finance evidence", Department: "finance", Method: "GET", Path: "/pages/finances", Global: true},
	{Name: "signals_read", Description: "Read the pipeline ranked by buying signals (hiring, funding, website gaps) with sources and openers", Department: "sales", Method: "GET", Path: "/pages/signals", Global: true},
	{Name: "dialer_read", Description: "Read dialer readiness and contact queue", Department: "sales", Method: "GET", Path: "/pages/dialer", Global: true},
	{Name: "apollo_enrich", Description: "Reveal phone data using Apollo credits after review", Department: "sales", Method: "POST", Path: "/pages/dialer/enrich", Keys: []string{"APOLLO_API_KEY"}, Approval: true, Global: true},
	{Name: "dialer_dnc", Description: "Add a reviewed do-not-call entry", Department: "sales", Method: "POST", Path: "/pages/dialer/dnc", Approval: true, Global: true},
	{Name: "contacts_tag", Description: "Apply a reviewed contact tag", Department: "comms", Method: "POST", Path: "/pages/contacts/tags", Approval: true, Global: true},
	{Name: "attribution_read", Description: "Read Trakyo attribution workspace", Department: "sales", Method: "GET", Path: "/pages/trakyo/workspace", Keys: []string{"TRAKYO_API_KEY"}, Global: true},
	{Name: "attribution_link", Description: "Create a reviewed tracking link", Department: "marketing-growth", Method: "POST", Path: "/pages/trakyo/links", Keys: []string{"TRAKYO_API_KEY"}, Approval: true, Global: true},
	{Name: "lusha_enrich", Description: "Reveal matched contact emails and phones; paid Lusha API request", Department: "sales", Provider: "lusha", Method: "POST", Path: "/v3/contacts/search-and-enrich", Keys: []string{"LUSHA_API_KEY"}, Approval: true},
	{Name: "higgsfield_generate", Description: "Submit a reviewed image/video generation; returns request receipt", Department: "marketing-growth", Provider: "higgsfield", Method: "POST", Keys: []string{"HIGGSFIELD_API_KEY"}, Approval: true},
	{Name: "higgsfield_status", Description: "Poll a generation created by this task", Department: "marketing-growth", Provider: "higgsfield", Method: "GET", Keys: []string{"HIGGSFIELD_API_KEY"}},
	{Name: "higgsfield_cancel", Description: "Cancel a reviewed generation created by this task", Department: "marketing-growth", Provider: "higgsfield", Method: "POST", Keys: []string{"HIGGSFIELD_API_KEY"}, Approval: true},
	{Name: "remotion_render", Description: "Submit a reviewed render to the owner-configured render service", Department: "marketing-growth", Provider: "remotion", Method: "POST", Path: "/render", Keys: []string{"REMOTION_RENDER_URL", "REMOTION_RENDER_TOKEN"}, Approval: true},
	{Name: "remotion_status", Description: "Poll this task's render job", Department: "marketing-growth", Provider: "remotion", Method: "GET", Keys: []string{"REMOTION_RENDER_URL", "REMOTION_RENDER_TOKEN"}},
	{Name: "remotion_cancel", Description: "Cancel this task's render job after review", Department: "marketing-growth", Provider: "remotion", Method: "POST", Keys: []string{"REMOTION_RENDER_URL", "REMOTION_RENDER_TOKEN"}, Approval: true},
}

func Find(name string) (Tool, bool) {
	for _, t := range Catalog {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
func Allowed(role, department string, t Tool) bool {
	if role == "CEO" || role == "Conductor Operations" {
		return true
	}
	department = strings.TrimPrefix(department, "dept-")
	if t.Name == "knowledge_query" || t.Name == "pipeline_read" {
		return true
	}
	if role == "Prospect Research" || role == "Sales Operations" || role == "Sales Enablement and Delivery" {
		return t.Department == "sales" || t.Department == "clients" || t.Department == "comms" || t.Name == "knowledge_capture"
	}
	return department == t.Department || (department == "sales" && (t.Department == "comms" || t.Department == "finance")) || (department == "clients" && (t.Department == "comms" || t.Department == "sales"))
}
