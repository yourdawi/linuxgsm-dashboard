package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Server IDs are referenced by user permissions, tags and the audit log, so a server must keep
// its ID. Discovery derives IDs from the order of /etc/passwd ("vhserver", then
// "vhserver@user2"), which changes when servers come and go. The IDs of host servers are
// therefore stored in server_ids.json, keyed by the script path. An ID stays reserved while its
// entry exists, also when the server is temporarily missing (an unmounted disk, for example);
// deleting a server through the dashboard releases it. Docker containers keep their names as IDs.

const serverIDsFileName = "server_ids.json"

func (im *InstanceManager) serverIDsPath() string {
	return filepath.Join(im.configDir, serverIDsFileName)
}

func serverIDKey(inst *GameServerInstance) string {
	return filepath.ToSlash(filepath.Join(inst.Dir, inst.Script))
}

func (im *InstanceManager) loadServerIDs() map[string]string {
	ids := make(map[string]string)
	data, err := os.ReadFile(im.serverIDsPath())
	if err != nil {
		return ids
	}
	if err := json.Unmarshal(data, &ids); err != nil {
		fmt.Printf("[WARNING] Ignoring invalid %s: %v\n", serverIDsFileName, err)
		return make(map[string]string)
	}
	return ids
}

func (im *InstanceManager) saveServerIDs(ids map[string]string) {
	data, err := json.MarshalIndent(ids, "", "  ")
	if err == nil {
		err = os.MkdirAll(im.configDir, 0700)
	}
	if err == nil {
		err = WriteFileAtomic(im.serverIDsPath(), data, 0600)
	}
	if err != nil {
		fmt.Printf("[WARNING] Failed to save %s: %v\n", serverIDsFileName, err)
	}
}

// assignStableIDs replaces the IDs proposed by discovery with the stored ones and stores the IDs
// of new servers. The caller holds im.mu.
func (im *InstanceManager) assignStableIDs(found []*GameServerInstance) {
	stored := im.loadServerIDs()
	reserved := make(map[string]bool, len(stored))
	for _, id := range stored {
		reserved[id] = true
	}
	used := make(map[string]bool)
	changed := false

	// Known servers first, so a new server can never take their ID.
	var fresh []*GameServerInstance
	for _, inst := range found {
		if inst.Layout != LayoutHost {
			used[inst.ID] = true
			continue
		}
		if id, ok := stored[serverIDKey(inst)]; ok && IsValidServerID(id) && !used[id] {
			inst.ID = id
			used[id] = true
			continue
		}
		fresh = append(fresh, inst)
	}
	for _, inst := range fresh {
		id := inst.ID
		if used[id] || reserved[id] || !IsValidServerID(id) {
			base := inst.Script
			if inst.User != "" {
				base = inst.Script + "@" + inst.User
			}
			id = base
			for n := 2; used[id] || reserved[id] || !IsValidServerID(id); n++ {
				id = fmt.Sprintf("%s-%d", base, n)
			}
		}
		inst.ID = id
		used[id] = true
		stored[serverIDKey(inst)] = id
		changed = true
	}
	if changed {
		im.saveServerIDs(stored)
	}
}

// releaseServerID forgets the stored ID and the tag of a deleted server, so a later server may
// use the ID again without inheriting anything.
func (im *InstanceManager) releaseServerID(srv GameServerInstance) {
	im.mu.Lock()
	defer im.mu.Unlock()
	ids := im.loadServerIDs()
	for key, id := range ids {
		if id == srv.ID {
			delete(ids, key)
		}
	}
	im.saveServerIDs(ids)

	tags := im.loadServerTags()
	if _, ok := tags[srv.ID]; ok {
		delete(tags, srv.ID)
		_ = im.saveServerTags(tags)
	}
}
