package crmsync

// HubSpot (private app token): contacts by email; emails, meetings and notes
// as engagements associated with the contact.

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type HubSpot struct {
	Token, Base string
}

func (h HubSpot) Name() string { return "hubspot" }

func (h HubSpot) base() string {
	if h.Base != "" {
		return h.Base
	}
	return "https://api.hubapi.com"
}

func (h HubSpot) hdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + h.Token}
}

func (h HubSpot) UpsertContact(ctx context.Context, c Contact) (string, error) {
	var found struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	search := map[string]any{"filterGroups": []any{map[string]any{"filters": []any{map[string]any{
		"propertyName": "email", "operator": "EQ", "value": c.Email}}}}, "limit": 1}
	// Search is a read even though HubSpot makes it a POST.
	if err := callRead(ctx, h.base()+"/crm/v3/objects/contacts/search", h.hdr(), search, &found); err != nil {
		return "", err
	}
	props := map[string]any{"email": c.Email, "firstname": c.FirstName, "lastname": c.LastName, "company": c.Company}
	if c.Phone != "" {
		props["phone"] = c.Phone
	}
	if len(found.Results) > 0 {
		id := found.Results[0].ID
		return id, call(ctx, http.MethodPatch, h.base()+"/crm/v3/objects/contacts/"+id, h.hdr(), map[string]any{"properties": props}, nil)
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := call(ctx, http.MethodPost, h.base()+"/crm/v3/objects/contacts", h.hdr(), map[string]any{"properties": props}, &made); err != nil {
		return "", err
	}
	if made.ID == "" {
		return "", fmt.Errorf("hubspot: no contact id returned")
	}
	return made.ID, nil
}

func (h HubSpot) engagement(ctx context.Context, object string, typeID int, contactID string, props map[string]any) error {
	return call(ctx, http.MethodPost, h.base()+"/crm/v3/objects/"+object, h.hdr(), map[string]any{
		"properties": props,
		"associations": []any{map[string]any{"to": map[string]any{"id": contactID},
			"types": []any{map[string]any{"associationCategory": "HUBSPOT_DEFINED", "associationTypeId": typeID}}}},
	}, nil)
}

func (h HubSpot) LogEmail(ctx context.Context, contactID string, e Email) error {
	return h.engagement(ctx, "emails", 198, contactID, map[string]any{"hs_timestamp": e.SentAt.UTC().Format(time.RFC3339),
		"hs_email_direction": "EMAIL", "hs_email_status": "SENT", "hs_email_subject": e.Subject, "hs_email_text": e.Body})
}

func (h HubSpot) LogMeeting(ctx context.Context, contactID string, m Meeting) error {
	at := m.StartsAt.UTC().Format(time.RFC3339)
	return h.engagement(ctx, "meetings", 200, contactID, map[string]any{"hs_timestamp": at, "hs_meeting_start_time": at,
		"hs_meeting_end_time": m.StartsAt.Add(30 * time.Minute).UTC().Format(time.RFC3339), "hs_meeting_title": m.Title,
		"hs_meeting_body": m.Body, "hs_meeting_outcome": "SCHEDULED"})
}

func (h HubSpot) AddNote(ctx context.Context, contactID, text string) error {
	return h.engagement(ctx, "notes", 202, contactID, map[string]any{"hs_timestamp": time.Now().UTC().Format(time.RFC3339), "hs_note_body": text})
}
