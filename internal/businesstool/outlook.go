package businesstool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"
	"time"
)

type Outlook struct {
	fixture string
	state   string
	folders []string
}

func NewOutlook(fixture, state string, folders []string) (*Outlook, error) {
	if !filepath.IsAbs(fixture) || !filepath.IsAbs(state) || filepath.Clean(fixture) == filepath.Clean(state) ||
		len(folders) == 0 || len(folders) > 10 {
		return nil, Invalid
	}
	for _, folder := range folders {
		if !reference.MatchString(folder) {
			return nil, Invalid
		}
	}
	return &Outlook{fixture, state, slices.Clone(folders)}, nil
}

type outlookFixture struct {
	Version    int              `json:"version"`
	Mailbox    string           `json:"mailbox"`
	Generation string           `json:"generation"`
	AsOf       time.Time        `json:"as_of"`
	Status     Status           `json:"status,omitempty"`
	Messages   []fixtureMessage `json:"messages"`
}

type fixtureMessage struct {
	Sequence   int64     `json:"sequence"`
	Folder     string    `json:"folder"`
	ID         string    `json:"id"`
	ThreadID   string    `json:"thread_id"`
	ReceivedAt time.Time `json:"received_at"`
	Subject    string    `json:"subject,omitempty"`
	Body       string    `json:"body,omitempty"`
}

type mailRef struct {
	ID         string    `json:"id"`
	ThreadID   string    `json:"thread_id"`
	ReceivedAt time.Time `json:"received_at"`
}

type mailItem struct {
	mailRef
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
}

type mailPage struct {
	Items      []mailItem `json:"items"`
	NextBefore string     `json:"next_before,omitempty"`
	Truncated  bool       `json:"truncated"`
}

type syncPage struct {
	Items       []mailRef `json:"items"`
	More        bool      `json:"more"`
	CursorReset bool      `json:"cursor_reset"`
}

type mailArgs struct {
	Folder string `json:"folder"`
	Limit  int    `json:"limit,omitempty"`
}

type mailListArgs struct {
	mailArgs
	Before string `json:"before,omitempty"`
}

type mailThreadArgs struct {
	mailArgs
	ThreadID string `json:"thread_id"`
}

type mailSyncArgs struct {
	mailArgs
	OperationID string `json:"operation_id"`
}

func (r Runner) runOutlook(ctx context.Context, call Call) (any, error) {
	switch call.Tool {
	case "outlook.messages.list":
		var args mailListArgs
		if decodeArgument(call.Arguments, &args) != nil || (args.Before != "" && !reference.MatchString(args.Before)) {
			return nil, Invalid
		}
		mailbox, limit, err := r.readMailbox(ctx, args.mailArgs)
		if err != nil {
			return nil, err
		}
		return mailbox.list(args.Folder, args.Before, limit)
	case "outlook.threads.read":
		var args mailThreadArgs
		if decodeArgument(call.Arguments, &args) != nil || !reference.MatchString(args.ThreadID) {
			return nil, Invalid
		}
		mailbox, limit, err := r.readMailbox(ctx, args.mailArgs)
		if err != nil {
			return nil, err
		}
		return mailbox.thread(args.Folder, args.ThreadID, limit)
	case "outlook.messages.sync":
		var args mailSyncArgs
		if decodeArgument(call.Arguments, &args) != nil || !isUUID(args.OperationID) {
			return nil, Invalid
		}
		mailbox, limit, err := r.readMailbox(ctx, args.mailArgs)
		if err != nil {
			return nil, err
		}
		scope := mailboxScope(r.Binding, args.Folder)
		return r.Outlook.sync(ctx, mailbox, scope, args.Folder, args.OperationID, limit)
	default:
		return nil, Invalid
	}
}

func (r Runner) readMailbox(ctx context.Context, args mailArgs) (outlookFixture, int, error) {
	limit, err := pageLimit(args.Limit)
	if err != nil || !slices.Contains(r.Outlook.folders, args.Folder) {
		return outlookFixture{}, 0, Invalid
	}
	if err := r.authorize(ctx, "read"); err != nil {
		return outlookFixture{}, 0, err
	}
	mailbox, err := r.Outlook.load(r.Binding.Target)
	return mailbox, limit, err
}

func (o Outlook) load(target string) (outlookFixture, error) {
	var fixture outlookFixture
	raw, err := readOwnedFile(o.fixture, 2<<20)
	if err != nil || decodeArgument(raw, &fixture) != nil || fixture.Version != 1 ||
		!reference.MatchString(fixture.Generation) || fixture.AsOf.IsZero() || len(fixture.Messages) > 3000 {
		return outlookFixture{}, Unavailable
	}
	if fixture.Mailbox != target {
		return outlookFixture{}, Denied
	}
	switch fixture.Status {
	case "":
	case ConsentRequired, Revoked, Throttled, StaleSync, Unavailable:
		return outlookFixture{}, fixture.Status
	default:
		return outlookFixture{}, Unavailable
	}
	var sequence int64
	for _, message := range fixture.Messages {
		if message.Sequence <= sequence || !reference.MatchString(message.Folder) ||
			!reference.MatchString(message.ID) || !reference.MatchString(message.ThreadID) ||
			message.ReceivedAt.IsZero() || message.ReceivedAt.After(fixture.AsOf) ||
			!boundedText(message.Subject, 400) || !boundedText(message.Body, maxReply) {
			return outlookFixture{}, Unavailable
		}
		sequence = message.Sequence
	}
	return fixture, nil
}

func (m fixtureMessage) ref() mailRef {
	return mailRef{m.ID, m.ThreadID, m.ReceivedAt}
}

func (f outlookFixture) visible(folder string) []fixtureMessage {
	seen := map[string]bool{}
	var messages []fixtureMessage
	cutoff := f.AsOf.AddDate(0, 0, -maxHistory)
	for i := len(f.Messages) - 1; i >= 0; i-- {
		m := f.Messages[i]
		if m.Folder == folder && !m.ReceivedAt.Before(cutoff) && !seen[m.ID] {
			seen[m.ID] = true
			messages = append(messages, m)
		}
	}
	return messages
}

func (f outlookFixture) list(folder, before string, limit int) (mailPage, error) {
	items := f.visible(folder)
	if before != "" {
		start := slices.IndexFunc(items, func(m fixtureMessage) bool { return m.ID == before })
		if start < 0 {
			return mailPage{}, StaleSync
		}
		items = items[start+1:]
	}
	page := mailPage{Items: []mailItem{}, Truncated: len(items) > limit}
	for _, message := range items[:min(limit, len(items))] {
		page.Items = append(page.Items, mailItem{mailRef: message.ref(), Subject: message.Subject})
	}
	if page.Truncated {
		page.NextBefore = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

func (f outlookFixture) thread(folder, threadID string, limit int) (mailPage, error) {
	page := mailPage{Items: []mailItem{}}
	for _, message := range f.visible(folder) {
		if message.ThreadID != threadID {
			continue
		}
		if len(page.Items) == limit {
			page.Truncated = true
			break
		}
		page.Items = append(page.Items, mailItem{message.ref(), message.Subject, message.Body})
	}
	if len(page.Items) == 0 {
		return mailPage{}, NotFound
	}
	slices.Reverse(page.Items)
	return page, nil
}

func mailboxScope(binding Binding, folder string) string {
	// Use the stable connection scope so revoking or replacing an individual
	// grant cannot restart another actor's mailbox sync.
	key, _ := json.Marshal([5]string{binding.TenantID, binding.WorkspaceID, binding.ConnectionID, binding.Target, folder})
	digest := sha256.Sum256(key)
	return hex.EncodeToString(digest[:])
}
