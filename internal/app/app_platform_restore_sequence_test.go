package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestPostgresAppPlatformFailedRestoreKeepsIdentityFloor(t *testing.T) {
	st := platformTestStore(t, true)
	// Begin from a healthy sequence, independently of prior failed-restore
	// fixtures. The failure under test happens only after this setup.
	mustExec(t, st, `SELECT setval(pg_get_serial_sequence('audit_log','id'),COALESCE((SELECT MAX(id) FROM audit_log),0)+1,false)`)
	addAudit := func() int64 {
		var id int64
		if err := st.queryRow(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES('fixture','system',0,'fixture','fixture','','{}','') RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	addAudit()
	content := []byte("fixture public artifact")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	platformTestInsert(t, st.poolExec(), "frontend_artifacts", map[string]any{"sha256": digest, "archive_bytes": len(content), "content": content, "created_ms": 1})
	mustExec(t, st, `UPDATE app_platform_frontend_artifacts SET content=?`, bytes.Repeat([]byte{'x'}, len(content)))
	bad := dumpOf(t, st)
	mustExec(t, st, `UPDATE app_platform_frontend_artifacts SET content=?`, content)
	addAudit()
	last := addAudit()
	before := dumpOf(t, st)
	if _, err := st.restoreFrom(bytes.NewReader(bad), true); err == nil {
		t.Fatal("bad artifact restored")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
		t.Fatal("failed restore changed target rows")
	}
	if next := addAudit(); next <= last {
		t.Fatal("failed restore rewound identity below live rows")
	}
}
