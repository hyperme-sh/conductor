package businesstool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Odoo struct {
	origin *url.URL
	token  string
	client *http.Client
}

func NewOdoo(origin, readerToken string, transport http.RoundTripper) (*Odoo, error) {
	target, err := originURL(origin)
	if err != nil || strings.TrimSpace(readerToken) == "" || strings.ContainsAny(readerToken, "\r\n") {
		return nil, Invalid
	}
	return &Odoo{target, readerToken, clientFor(transport)}, nil
}

type Lead struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContactName string `json:"contact_name,omitempty"`
	Stage       string `json:"stage,omitempty"`
	Owner       string `json:"owner,omitempty"`
	NextAction  string `json:"next_action,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	Version     string `json:"version"`
}

type Case struct {
	ID              string    `json:"id"`
	Subject         string    `json:"subject"`
	Status          string    `json:"status"`
	UpdatedAt       string    `json:"updated_at,omitempty"`
	AssigneeSubject string    `json:"assignee_subject,omitempty"`
	DeliveryStatus  string    `json:"delivery_status,omitempty"`
	SourceReference string    `json:"source_reference,omitempty"`
	Version         string    `json:"version"`
	Messages        []Message `json:"messages,omitempty"`
}

type Message struct {
	ID        string `json:"id"`
	Actor     string `json:"actor"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type leadList struct {
	Items []Lead `json:"items"`
}

type caseList struct {
	Cases      []Case  `json:"cases"`
	NextCursor *string `json:"next_cursor"`
}

type searchArgs struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type caseSearchArgs struct {
	searchArgs
	Cursor string `json:"cursor,omitempty"`
}

type readArgs struct {
	ID string `json:"id"`
}

type draftArgs struct {
	ID              string `json:"id"`
	ExpectedVersion string `json:"expected_version"`
	Action          string `json:"action"`
	Text            string `json:"text"`
}

type proposal struct {
	OperationID     string `json:"operation_id"`
	Resource        string `json:"resource"`
	SourceID        string `json:"source_id"`
	ExpectedVersion string `json:"expected_version"`
	Action          string `json:"action"`
	Text            string `json:"text"`
}

func (r Runner) runOdoo(ctx context.Context, call Call) (any, error) {
	switch call.Tool {
	case "odoo.leads.search":
		var args searchArgs
		if decodeArgument(call.Arguments, &args) != nil {
			return nil, Invalid
		}
		return r.searchLeads(ctx, args)
	case "odoo.cases.search":
		var args caseSearchArgs
		if decodeArgument(call.Arguments, &args) != nil || !boundedText(args.Cursor, 512) {
			return nil, Invalid
		}
		return r.searchCases(ctx, args)
	case "odoo.leads.read", "odoo.cases.read":
		var args readArgs
		lead := call.Tool == "odoo.leads.read"
		if decodeArgument(call.Arguments, &args) != nil || !recordID(args.ID, lead) {
			return nil, Invalid
		}
		return r.readRecord(ctx, args.ID, lead)
	case "odoo.leads.draft", "odoo.cases.draft":
		var args draftArgs
		lead := call.Tool == "odoo.leads.draft"
		if decodeArgument(call.Arguments, &args) != nil {
			return nil, Invalid
		}
		return r.draft(ctx, args, lead)
	default:
		return nil, Invalid
	}
}

func recordID(id string, lead bool) bool {
	if lead {
		return leadID.MatchString(id)
	}
	return id != "." && id != ".." && reference.MatchString(id)
}

func (o Odoo) get(ctx context.Context, scope, path string, query url.Values, into any) error {
	target := *o.origin
	target.Path = path
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Unavailable
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	req.Header.Set("X-Support-Workspace", scope)
	return getJSON(o.client, req, 1<<20, into)
}

func queryValues(args searchArgs) (url.Values, int, error) {
	limit, err := pageLimit(args.Limit)
	if err != nil || !boundedText(args.Query, maxQuery) {
		return nil, 0, Invalid
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if args.Query != "" {
		query.Set("q", args.Query)
	}
	return query, limit, nil
}

func (r Runner) searchLeads(ctx context.Context, args searchArgs) (leadList, error) {
	var data leadList
	query, limit, err := queryValues(args)
	if err != nil {
		return data, err
	}
	if err := r.authorize(ctx, "read"); err != nil {
		return data, err
	}
	if err := r.Odoo.get(ctx, r.Binding.WorkspaceID, "/v1/crm/leads", query, &data); err != nil {
		return leadList{}, err
	}
	if data.Items == nil || len(data.Items) > limit {
		return leadList{}, Unavailable
	}
	for _, item := range data.Items {
		if !validLead(item) {
			return leadList{}, Unavailable
		}
	}
	return data, nil
}

func (r Runner) searchCases(ctx context.Context, args caseSearchArgs) (caseList, error) {
	var data caseList
	query, limit, err := queryValues(args.searchArgs)
	if err != nil {
		return data, err
	}
	if args.Cursor != "" {
		query.Set("cursor", args.Cursor)
	}
	if err := r.authorize(ctx, "read"); err != nil {
		return data, err
	}
	if err := r.Odoo.get(ctx, r.Binding.WorkspaceID, "/v1/support/cases", query, &data); err != nil {
		return caseList{}, err
	}
	if data.Cases == nil || len(data.Cases) > limit || (data.NextCursor != nil && !boundedText(*data.NextCursor, 512)) {
		return caseList{}, Unavailable
	}
	for i := range data.Cases {
		data.Cases[i].Messages = nil
		if !validCase(data.Cases[i]) {
			return caseList{}, Unavailable
		}
	}
	return data, nil
}

func (r Runner) readRecord(ctx context.Context, id string, lead bool) (any, error) {
	if err := r.authorize(ctx, "read"); err != nil {
		return nil, err
	}
	if lead {
		var result struct {
			Lead Lead `json:"lead"`
		}
		if err := r.Odoo.get(ctx, r.Binding.WorkspaceID, "/v1/crm/leads/"+id, nil, &result); err != nil {
			return nil, err
		}
		if result.Lead.ID != id || !validLead(result.Lead) {
			return nil, Unavailable
		}
		return result.Lead, nil
	}
	var result struct {
		Case Case `json:"case"`
	}
	if err := r.Odoo.get(ctx, r.Binding.WorkspaceID, "/v1/support/cases/"+id, nil, &result); err != nil {
		return nil, err
	}
	if result.Case.ID != id || !validCase(result.Case) {
		return nil, Unavailable
	}
	return result.Case, nil
}

func versionValid(value string) bool {
	return strings.TrimSpace(value) != "" && boundedText(value, 200)
}

func validLead(item Lead) bool {
	return recordID(item.ID, true) && versionValid(item.Version) &&
		boundedText(item.Name, 400) && boundedText(item.ContactName, 400) &&
		boundedText(item.Stage, 160) && boundedText(item.Owner, 200) &&
		boundedText(item.NextAction, 2000) && boundedText(item.UpdatedAt, 80)
}

func validCase(item Case) bool {
	if !recordID(item.ID, false) || !versionValid(item.Version) ||
		!boundedText(item.Subject, 400) || !boundedText(item.Status, 80) ||
		!boundedText(item.UpdatedAt, 80) || !boundedText(item.AssigneeSubject, 200) ||
		!boundedText(item.DeliveryStatus, 80) || !boundedText(item.SourceReference, 512) || len(item.Messages) > maxLimit {
		return false
	}
	for _, message := range item.Messages {
		if !recordID(message.ID, false) || (message.Actor != "customer" && message.Actor != "operator") ||
			!boundedText(message.Body, maxReply) || !boundedText(message.CreatedAt, 80) {
			return false
		}
	}
	return true
}

func (r Runner) draft(ctx context.Context, args draftArgs, lead bool) (proposal, error) {
	if !recordID(args.ID, lead) || !versionValid(args.ExpectedVersion) ||
		strings.TrimSpace(args.Text) == "" || !boundedText(args.Text, maxDraft) {
		return proposal{}, Invalid
	}
	var resource string
	switch {
	case lead && (args.Action == "route" || args.Action == "follow_up"):
		resource = "lead"
	case !lead && args.Action == "reply":
		resource = "case"
	default:
		return proposal{}, Invalid
	}
	item, err := r.readRecord(ctx, args.ID, lead)
	if err != nil {
		return proposal{}, err
	}
	var version string
	switch source := item.(type) {
	case Lead:
		version = source.Version
	case Case:
		version = source.Version
	}
	if version != args.ExpectedVersion {
		return proposal{}, StaleRecord
	}
	if err := r.authorize(ctx, "draft"); err != nil {
		return proposal{}, err
	}
	// Content and scope bind the identifier, so a retry of the same proposal
	// returns the same identifier without storing a draft or writing to Odoo.
	key, err := json.Marshal(struct {
		Binding  Binding
		Resource string
		Draft    draftArgs
	}{r.Binding, resource, args})
	if err != nil {
		return proposal{}, Unavailable
	}
	digest := sha256.Sum256(append([]byte("conductor.business-proposal.v1\n"), key...))
	return proposal{hex.EncodeToString(digest[:]), resource, args.ID, version, args.Action, args.Text}, nil
}
