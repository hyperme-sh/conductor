package accesspreview

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func typed(operation, target string, args any) Request {
	p := sample().Preview
	p.Action.Operation = operation
	p.Action.Target = target
	raw, _ := json.Marshal(args)
	p.Action.ArgumentsJSON = string(raw)
	p.Action.Arguments = fmt.Sprintf("%x", sha256.Sum256(raw))
	return p
}
func TestTypedArgumentsRefuseExpansion(t *testing.T) {
	h := strings.Repeat("a", 64)
	network := HTTPSArguments{Method: "GET", Addresses: []string{"8.8.8.8"}, BodyHash: fmt.Sprintf("%x", sha256.Sum256(nil))}
	if !validTyped(typed("https", "https://example.test/api", network)) {
		t.Fatal("valid network refused")
	}
	for _, addr := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:8.8.8.8"} {
		network.Addresses = []string{addr}
		if validTyped(typed("https", "https://example.test/api", network)) {
			t.Fatal(addr)
		}
	}
	w := WriteArguments{ContentHash: h, ContentBytes: 12, ObjectHash: h, ParentHash: h}
	if !validTyped(typed("write", "/project/file", w)) {
		t.Fatal("valid write refused")
	}
	for _, target := range []string{"/outside/file", "/project/../file", "/project//file"} {
		if validTyped(typed("write", target, w)) {
			t.Fatal(target)
		}
	}
	service := ServiceArguments{Verb: "restart", Executable: "/usr/bin/systemctl", ExecutableHash: h, Unit: "example.service", UID: 1000, ControllerHash: h}
	if !validTyped(typed("service_restart", "example.service", service)) {
		t.Fatal("valid service refused")
	}
	service.UID = 0
	if validTyped(typed("service_restart", "example.service", service)) {
		t.Fatal("root control accepted")
	}
	p := typed("write", "/project/file", w)
	p.Action.ArgumentsJSON += " "
	if validTyped(p) {
		t.Fatal("changed bytes accepted")
	}
	if uniqueArguments(`{"method":"GET","method":"POST"}`) {
		t.Fatal("duplicate keys accepted")
	}
	p = typed("read", "/project/file", w)
	if validTyped(p) {
		t.Fatal("read grant expanded")
	}
}
