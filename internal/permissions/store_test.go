package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

func testRequest() Request {
	return Request{Provider: "sinores", SessionID: "ses_test", RequestID: "req_test", Permissions: []RequestedPermission{
		{ID: RuntimeWake, Title: "Wake", Risk: "runtime", Delegation: DelegationBackground},
		{ID: "messages.read_new", Title: "Read new messages", Risk: "read", Delegation: DelegationBackground, MCPTools: []string{"get_new_messages"}},
		{ID: "payments.send", Title: "Send payment", Risk: "sensitive", Delegation: DelegationInteractiveOnly, MCPTools: []string{"send_payment"}},
	}}
}

func TestGrantAuthorizeAndChangedManifest(t *testing.T) {
	store := NewStore()
	if _, err := RecordRequest(&store, testRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := GrantPermissions(&store, "sinores", "ses_test", ScopeBinding, []string{RuntimeWake, "messages.read_new"}); err != nil {
		t.Fatal(err)
	}
	authorization, err := Authorize(&store, "sinores", "ses_test", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorization.MCPTools) != 1 || authorization.MCPTools[0] != "get_new_messages" {
		t.Fatalf("authorization=%#v", authorization)
	}
	changed := testRequest()
	changed.Permissions[1].MCPTools = []string{"different_tool"}
	if _, err := RecordRequest(&store, changed); err != nil {
		t.Fatal(err)
	}
	changedAuthorization, err := Authorize(&store, "sinores", "ses_test", true)
	if err != nil { t.Fatal(err) }
	if len(changedAuthorization.MCPTools) != 0 || len(changedAuthorization.Permissions) != 1 || changedAuthorization.Permissions[0] != RuntimeWake {
		t.Fatalf("changed permission definition retained an old tool grant: %#v", changedAuthorization)
	}
}

func TestInteractiveOnlyAndOneTimeGrant(t *testing.T) {
	store := NewStore()
	if _, err := RecordRequest(&store, testRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := GrantPermissions(&store, "sinores", "ses_test", ScopeBinding, []string{RuntimeWake, "payments.send"}); err == nil {
		t.Fatal("interactive-only permission was delegated")
	}
	if _, err := GrantPermissions(&store, "sinores", "ses_test", ScopeOnce, []string{RuntimeWake}); err != nil {
		t.Fatal(err)
	}
	if _, err := Authorize(&store, "sinores", "ses_test", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Authorize(&store, "sinores", "ses_test", true); err == nil {
		t.Fatal("one-time grant was not consumed")
	}
}

func TestStoreIsPrivate(t *testing.T) {
	store := NewStore()
	if _, err := RecordRequest(&store, testRequest()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nested", "permissions.json")
	if err := Save(path, store); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Requests) != 1 {
		t.Fatalf("loaded=%#v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}
