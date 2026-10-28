package backend

import (
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func scanIDs(t *testing.T, im *InstanceManager, found ...GameServerInstance) map[string]string {
	t.Helper()
	im.rt = discoverRuntime{&fakeRuntime{}, found}
	im.ScanInstances()
	byUser := make(map[string]string)
	for id, inst := range im.instances {
		byUser[inst.User] = id
	}
	return byUser
}

// Discovery proposes "ts3server" for the first server in /etc/passwd and "ts3server@<user>" for
// the others. The stored IDs keep every server's ID when that order changes.
func TestServerIDsStayStable(t *testing.T) {
	im := NewStaticInstanceManager(t.TempDir())
	im.static = false
	srv := func(user, id string) GameServerInstance {
		return GameServerInstance{ID: id, Script: "ts3server", User: user, Dir: "/home/" + user}
	}

	ids := scanIDs(t, im, srv("a", "ts3server"), srv("b", "ts3server@b"))
	if ids["a"] != "ts3server" || ids["b"] != "ts3server@b" {
		t.Fatalf("first scan must keep the proposed IDs: %v", ids)
	}

	// a disappears without the dashboard (removed by hand): b is now proposed as "ts3server".
	ids = scanIDs(t, im, srv("b", "ts3server"))
	if ids["b"] != "ts3server@b" {
		t.Fatalf("b changed its ID: %v", ids)
	}
	// A new server must not take a's reserved ID.
	ids = scanIDs(t, im, srv("c", "ts3server"), srv("b", "ts3server@b"))
	if ids["c"] != "ts3server@c" || ids["b"] != "ts3server@b" {
		t.Fatalf("reserved ID reused: %v", ids)
	}
	// Once a is deleted through the dashboard, its ID is free again.
	im.releaseServerID(GameServerInstance{ID: "ts3server"})
	ids = scanIDs(t, im, srv("d", "ts3server"), srv("b", "ts3server@b"), srv("c", "ts3server@c"))
	if ids["d"] != "ts3server" || ids["c"] != "ts3server@c" {
		t.Fatalf("after release: %v", ids)
	}

	// Docker containers keep their names.
	im.rt = discoverRuntime{&fakeRuntime{}, []GameServerInstance{{ID: "vh1", Script: "vhserver", Dir: "/data/vh1", Layout: LayoutData}}}
	im.ScanInstances()
	if _, ok := im.instances["vh1"]; !ok {
		t.Fatal("container ID changed")
	}
}

func TestDeleteServerReleasesIDAndTag(t *testing.T) {
	im := NewStaticInstanceManager(t.TempDir())
	im.static = false
	rt := &fakeRuntime{}
	found := []GameServerInstance{
		{ID: "ts3server", Script: "ts3server", User: "a", Dir: "/home/a"},
		{ID: "ts3server@b", Script: "ts3server", User: "b", Dir: "/home/b"},
	}
	im.rt = discoverRuntime{rt, found}
	im.ScanInstances()
	if err := im.SetServerTag("ts3server", "Cluster A"); err != nil {
		t.Fatal(err)
	}

	im.rt = discoverRuntime{rt, found[1:]} // the runtime has removed a's server
	if !im.DeleteServer(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), "ts3server") {
		t.Fatal("delete failed")
	}
	if _, ok := im.loadServerTags()["ts3server"]; ok {
		t.Error("tag of the deleted server kept")
	}
	var stored []string
	for _, id := range im.loadServerIDs() {
		stored = append(stored, id)
	}
	sort.Strings(stored)
	if strings.Join(stored, ",") != "ts3server@b" {
		t.Errorf("stored IDs = %v", stored)
	}
	if im.DeleteServer(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), "missing") {
		t.Error("deleting an unknown server reported success")
	}
}
