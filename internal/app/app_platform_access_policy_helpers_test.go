package app

import (
	"strconv"
	"testing"
)

// platformOpenTargets opens an installation to the Default OU, and so to every user: the install
// plan's open_to_ous for a fixture that needs its actor to be able to use the app right after
// activation (doc 190, D7).
func platformOpenTargets(t *testing.T, s *Server) []platformAudienceTarget {
	t.Helper()
	snapshot, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil || snapshot.graph.defaultID == 0 {
		t.Fatal("default OU", err)
	}
	return []platformAudienceTarget{{kind: "ou", ouID: snapshot.graph.defaultID}}
}

// platformOpenAudience is the resolved form of platformOpenTargets, as a stored plan carries it.
func platformOpenAudience(t *testing.T, s *Server) []platformInstallAudience {
	t.Helper()
	snapshot, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil || snapshot.graph.defaultID == 0 {
		t.Fatal("default OU", err)
	}
	id := snapshot.graph.defaultID
	return []platformInstallAudience{{Type: "ou", GroupID: strconv.FormatInt(id, 10), GroupCreated: snapshot.graph.nodes[id].created}}
}

// platformSetAccess writes one app_ou_access row for an installation, as an administrator's edit does.
func platformSetAccess(t *testing.T, s *Server, installation string, ou int64, enabled bool) {
	t.Helper()
	mustExec(t, s.st, `DELETE FROM app_ou_access WHERE app_key=? AND group_id=?`, platformAppKey(installation), ou)
	mustExec(t, s.st, `INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES(?,?,?)`, platformAppKey(installation), ou, boolInt(enabled))
}

// platformCloseAccess removes every OU row of the installation, which closes it for everyone.
func platformCloseAccess(t *testing.T, s *Server, installation string) {
	t.Helper()
	mustExec(t, s.st, `DELETE FROM app_ou_access WHERE app_key=?`, platformAppKey(installation))
}

// platformOpenDefault opens the installation to the Default OU, so every user can use it.
func platformOpenDefault(t *testing.T, s *Server, installation string) {
	t.Helper()
	snapshot, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil || snapshot.graph.defaultID == 0 {
		t.Fatal("default OU", err)
	}
	platformSetAccess(t, s, installation, snapshot.graph.defaultID, true)
}
