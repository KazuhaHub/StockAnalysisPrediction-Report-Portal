package app

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
)

const defaultLoginHistoryKeep = 100
const maxLoginHistoryKeep = 10000
const setLoginHistoryKeep = "login_history_keep"

func loginHistoryKeep(e migExec, exclusive bool) (int, error) {
	if _, err := e.exec(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO NOTHING`, setLoginHistoryKeep, strconv.Itoa(defaultLoginHistoryKeep)); err != nil {
		return 0, err
	}
	q := `SELECT v FROM meta WHERE k=?`
	if e.s.driver == "postgres" {
		if exclusive {
			q += " FOR UPDATE"
		} else {
			q += " FOR SHARE"
		}
	}
	var raw string
	if err := e.queryRow(q, setLoginHistoryKeep).Scan(&raw); err != nil {
		return 0, err
	}
	keep, err := strconv.Atoi(raw)
	if err != nil || keep < 1 || keep > maxLoginHistoryKeep {
		keep = defaultLoginHistoryKeep
	}
	return keep, nil
}

// Serialize an account's history writes on its users row in Postgres. SQLite's
// single connection serializes the transaction. Initialization runs once, so rows
// removed from personal history cannot return from the administrator's audit log.
func prepareLoginHistory(e migExec, user string) (int, error) {
	keep, err := loginHistoryKeep(e, false)
	if err != nil {
		return 0, err
	}
	initialized := 0
	q := `SELECT login_history_initialized FROM users WHERE username=?`
	if e.s.driver == "postgres" {
		q += " FOR UPDATE"
	}
	err = e.queryRow(q, user).Scan(&initialized)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if initialized == 0 {
		if _, err := e.exec(`INSERT INTO login_history(id,username,at,ip,detail)
			SELECT id,actor,at,COALESCE(ip,''),COALESCE(detail,'') FROM audit_log
			WHERE actor=? AND action=? AND target_type='user' AND target_id=?
			ORDER BY id DESC LIMIT ? ON CONFLICT(id) DO NOTHING`, user, AuditLogin, user, keep); err != nil {
			return 0, err
		}
		if _, err := e.exec(`UPDATE users SET login_history_initialized=1 WHERE username=?`, user); err != nil {
			return 0, err
		}
	}
	return keep, nil
}

func pruneLoginHistory(e migExec, user string, keep int) error {
	_, err := e.exec(`DELETE FROM login_history WHERE username=? AND id NOT IN
		(SELECT id FROM login_history WHERE username=? ORDER BY id DESC LIMIT ?)`, user, user, keep)
	return err
}

func (s *Store) writeLoginAudit(e AuditEntry, at string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m := migExec{s: s, ex: tx}
	keep, err := prepareLoginHistory(m, e.Actor)
	if err != nil {
		return err
	}
	var id int64
	q := `INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,?,?,?,?)`
	args := []any{at, e.Actor, e.ActorOU, e.Action, e.TargetType, e.TargetID, e.Detail, e.IP}
	if s.driver == "postgres" {
		err = m.queryRow(q+" RETURNING id", args...).Scan(&id)
	} else {
		var res sql.Result
		res, err = m.exec(q, args...)
		if err == nil {
			id, err = res.LastInsertId()
		}
	}
	if err != nil {
		return err
	}
	if _, err = m.exec(`INSERT INTO login_history(id,username,at,ip,detail) VALUES(?,?,?,?,?)`, id, e.Actor, at, e.IP, e.Detail); err != nil {
		return err
	}
	if err = pruneLoginHistory(m, e.Actor, keep); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) listLoginHistory(user string, limit int) ([]AuditEntry, int, int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, 0, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	keep, err := prepareLoginHistory(e, user)
	if err != nil {
		return nil, 0, 0, err
	}
	if err = pruneLoginHistory(e, user, keep); err != nil {
		return nil, 0, 0, err
	}
	var total int
	if err = e.queryRow(`SELECT COUNT(*) FROM login_history WHERE username=?`, user).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	rows, err := e.query(`SELECT id,at,ip,detail FROM login_history WHERE username=? ORDER BY id DESC LIMIT ?`, user, limit)
	if err != nil {
		return nil, 0, 0, err
	}
	items := []AuditEntry{}
	for rows.Next() {
		var item AuditEntry
		if err := rows.Scan(&item.ID, &item.At, &item.IP, &item.Detail); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, 0, err
	}
	return items, total, keep, tx.Commit()
}

func (s *Server) apiLoginHistoryRetention(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Keep *int `json:"keep"`
	}
	if err := readJSON(r, &in); err != nil || in.Keep == nil || *in.Keep < 1 || *in.Keep > maxLoginHistoryKeep {
		jsonError(w, 400, fmt.Sprintf("keep must be between 1 and %d", maxLoginHistoryKeep))
		return
	}
	tx, err := s.st.db.Begin()
	if err != nil {
		jsonError(w, 500, "could not save retention")
		return
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	if _, err = loginHistoryKeep(e, true); err == nil {
		_, err = e.exec(`UPDATE meta SET v=? WHERE k=?`, strconv.Itoa(*in.Keep), setLoginHistoryKeep)
	}
	// Lock policy before account rows, matching sign-in writes. This waits for
	// in-flight writes and makes pruning all accounts atomic with the policy change.
	if err == nil {
		var rows *sql.Rows
		rows, err = e.query(`SELECT username FROM users ORDER BY username`)
		var users []string
		if err == nil {
			for rows.Next() {
				var name string
				if err = rows.Scan(&name); err != nil {
					break
				}
				users = append(users, name)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		if err == nil {
			for _, name := range users {
				if _, err = prepareLoginHistory(e, name); err != nil {
					break
				}
				if err = pruneLoginHistory(e, name, *in.Keep); err != nil {
					break
				}
			}
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		jsonError(w, 500, "could not save retention")
		return
	}
	s.recordChange(r, user, AuditLoginHistoryRetention, "login_history_retention", "", map[string]any{"keep": *in.Keep})
	writeJSON(w, okJSON)
}

func (s *Server) apiLoginHistoryRetentionGet(w http.ResponseWriter, r *http.Request, user string) {
	raw := s.st.GetSetting(setLoginHistoryKeep, strconv.Itoa(defaultLoginHistoryKeep))
	keep, err := strconv.Atoi(raw)
	if err != nil || keep < 1 || keep > maxLoginHistoryKeep {
		keep = defaultLoginHistoryKeep
	}
	writeJSON(w, map[string]any{"keep": keep})
}
