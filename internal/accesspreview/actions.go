package accesspreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

type HTTPSArguments struct {
	Method    string   `json:"method"`
	Addresses []string `json:"addresses"`
	BodyHash  string   `json:"body_sha256"`
	BodyBytes uint64   `json:"body_bytes"`
}
type WriteArguments struct {
	ContentHash  string `json:"content_sha256"`
	ContentBytes uint64 `json:"content_bytes"`
	ObjectHash   string `json:"expected_object_sha256"`
	ParentHash   string `json:"parent_object_sha256"`
}
type ServiceArguments struct {
	Verb           string `json:"verb"`
	Executable     string `json:"executable"`
	ExecutableHash string `json:"executable_sha256"`
	Unit           string `json:"unit"`
	UID            uint32 `json:"uid"`
	ControllerHash string `json:"controller_binding_sha256"`
}

func hash(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && strings.ToLower(v) == v
}
func canonicalFile(v string) bool {
	return len(v) > 1 && len(v) <= 4096 && path.IsAbs(v) && path.Clean(v) == v && !strings.ContainsAny(v, "*?\\") && !strings.ContainsFunc(v, func(c rune) bool { return c < 32 || c == 127 })
}
func uniqueArguments(raw string) bool {
	// Typed argument objects are flat; reject duplicate keys before decoding.
	d := json.NewDecoder(strings.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return false
		}
		key, ok := t.(string)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
	}
	_, e = d.Token()
	return e == nil
}
func validTyped(p Request) bool {
	a := p.Action
	if len(a.ArgumentsJSON) < 2 || len(a.ArgumentsJSON) > 16384 || !uniqueArguments(a.ArgumentsJSON) || !hash(a.Arguments) || !canonicalFile(p.Project) || a.Limits.Bytes > 1048576 || a.Limits.Seconds > 30 || p.ExpectedPolicy != "" && !id(p.ExpectedPolicy) {
		return false
	}
	sum := sha256.Sum256([]byte(a.ArgumentsJSON))
	if hex.EncodeToString(sum[:]) != a.Arguments {
		return false
	}
	raw := []byte(a.ArgumentsJSON)
	switch a.Operation {
	case "https":
		var args HTTPSArguments
		if decode(raw, &args) != nil || args.Method != "GET" && args.Method != "POST" || !hash(args.BodyHash) || args.BodyBytes > a.Limits.Bytes || len(args.Addresses) < 1 || len(args.Addresses) > 16 {
			return false
		}
		empty := sha256.Sum256(nil)
		if args.Method == "GET" && (args.BodyBytes != 0 || args.BodyHash != hex.EncodeToString(empty[:])) {
			return false
		}
		u, e := url.Parse(a.Target)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Host != u.Hostname() || u.Host != strings.ToLower(u.Host) || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || path.Clean(u.Path) != u.Path || strings.ContainsAny(a.Target, "%*\\") {
			return false
		}
		seen := map[string]bool{}
		for _, v := range args.Addresses {
			ip, e := netip.ParseAddr(v)
			if e != nil || ip.String() != v || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Is4In6() || seen[v] {
				return false
			}
			seen[v] = true
		}
		return true
	case "write":
		var args WriteArguments
		return canonicalFile(a.Target) && strings.HasPrefix(a.Target, p.Project+"/") && decode(raw, &args) == nil && hash(args.ContentHash) && hash(args.ObjectHash) && hash(args.ParentHash) && args.ContentBytes <= a.Limits.Bytes
	case "service_restart":
		var args ServiceArguments
		return decode(raw, &args) == nil && args.Verb == "restart" && args.Unit == a.Target && regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,120}\.service$`).MatchString(args.Unit) && canonicalFile(args.Executable) && hash(args.ExecutableHash) && hash(args.ControllerHash) && args.UID > 0
	}
	return false
}

// Admit uses the existing durable dispatch journal before any side effect.
// There is no retry or deletion path for consumed operation IDs.
func (c Client) Admit(ctx context.Context, raw []byte, journal interface{ Reserve(string, string) error }) (Result, error) {
	if journal == nil {
		return Result{}, Refused
	}
	result, err := c.ReviewAction(ctx, raw)
	if err != nil {
		return Result{}, err
	}
	if !result.Permitted {
		return result, nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return Result{}, Refused
	}
	digest := sha256.Sum256(encoded)
	if journal.Reserve(result.RequestID, hex.EncodeToString(digest[:])) != nil {
		return Result{}, Refused
	}
	if ctx.Err() != nil || result.Expires <= float64(time.Now().UnixNano())/1e9 {
		return Result{}, Refused
	}
	admitted, err := c.consume(ctx, raw, result.PolicyRevision, "action-admit")
	if err != nil || !admitted.Permitted || !admitted.DispatchAdmitted {
		return Result{}, Refused
	}
	admitted.Reserved = true
	return admitted, nil
}

func (c Client) consume(ctx context.Context, raw []byte, revision, mode string) (Result, error) {
	var envelope Envelope
	if decode(raw, &envelope) != nil {
		return Result{}, Refused
	}
	envelope.Preview.ExpectedPolicy = revision
	updated, err := json.Marshal(envelope)
	if err != nil {
		return Result{}, Refused
	}
	return c.review(ctx, updated, mode)
}

func (c Client) AdmitRead(ctx context.Context, raw []byte) (Result, error) {
	result, err := c.ReviewRead(ctx, raw)
	if err != nil || !result.Permitted {
		return result, err
	}
	return c.consume(ctx, raw, result.PolicyRevision, "read-admit")
}
