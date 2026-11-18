package accesspreview

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDreamBindsCurrentIdentityAndRejectsBroadening(t *testing.T) {
	for _, bad := range []bool{false, true} {
		p := sample().Preview
		owner, org := uuid.NewString(), uuid.NewString()
		record := DreamRecord{RuleHash: strings.Repeat("b", 64), PolicyRevision: uuid.NewString(), Action: p.Action, LastUse: 1, Expires: 9999999999, Pending: true}
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/machine/access-context") {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				json.NewEncoder(w).Encode(machineingress.Fact{Version: 1, Request: body["request_id"].(string), Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session, Expires: float64(time.Now().UnixNano())/1e9 + 10})
				return
			}
			if r.URL.Path != "/api/v1/aiuditor/access/dream" || r.Header.Get("Authorization") != "Bearer hm_msi_private" {
				t.Error("wrong authority")
			}
			result := DreamResult{Version: 1, RequestID: p.RequestID, Project: p.Project, PolicyRevision: uuid.NewString(), Identity: Identity{Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session}, Outcomes: []DreamOutcome{{RuleHash: record.RuleHash, Kind: "narrow", Status: "applied", Reason: "per use", Before: p.Action}}}
			if bad {
				changed := p.Action
				changed.Target = "/other"
				result.Outcomes[0].After = &changed
			}
			json.NewEncoder(w).Encode(result)
		}))
		client := Client{Plane: machineingress.Client{Origin: server.URL, HTTP: server.Client()}, Endpoint: server.URL + "/api/v1/aiuditor/access/dream", HTTP: server.Client()}
		raw, _ := json.Marshal(map[string]any{"credential": "hm_msi_private", "dream": DreamRequest{Binding: p.Binding, RequestID: p.RequestID, Project: p.Project, Records: []DreamRecord{record}}})
		_, err := client.Dream(context.Background(), raw)
		server.Close()
		if (err != nil) != bad {
			t.Fatal(bad, err)
		}
	}
}
