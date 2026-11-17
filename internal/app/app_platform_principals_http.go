package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// platformPrincipalAnswer turns the current directory into the response document. It runs inside
// the access transaction and must not write or call out.
type platformPrincipalAnswer func(platformPrincipalDirectory) any

// platformPrincipalParse validates one route's body and returns the launch binding the caller
// holds and how to answer. ok is false for any malformed input (400).
type platformPrincipalParse func(raw []byte) (binding platformPrincipalBinding, answer platformPrincipalAnswer, ok bool)

func strictPlatformPrincipalJSON(raw []byte, into any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(into) != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

// platformPrincipalCall is the shared front of both routes: path and origin, session, document
// budget, body, then the access transaction (which applies the per viewer rate once the caller is
// known to be allowed). The answer stays buffered until that
// transaction's final session and trust checks commit, and the audit row is written before it is sent.
func (s *Server) platformPrincipalCall(w http.ResponseWriter, r *http.Request, user, route string, parse platformPrincipalParse) {
	installation := r.PathValue("installation")
	if !platformHTTPPath(r) || !platformValidUUID(installation) || platformManagementOrigin.Check(r) != nil {
		platformDocumentFailure(w)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		jsonErrorCode(w, 401, "session_expired", "session unavailable")
		return
	}
	release, ok := s.platformDocuments.acquire()
	if !ok {
		platformJSONBusy(w)
		return
	}
	defer release()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPlatformPrincipalRequestBytes))
	if err != nil || len(raw) == 0 {
		platformPrincipalInvalid(w)
		return
	}
	defer clear(raw)
	binding, answer, ok := parse(raw)
	if !ok || !binding.valid() {
		platformPrincipalInvalid(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), platformDocumentTimeout)
	defer cancel()
	var document any
	group, err := s.platformPrincipalAccess(ctx, session, installation, binding, func(directory platformPrincipalDirectory) error {
		document = answer(directory)
		return nil
	})
	if err != nil {
		platformPrincipalFailure(w, err)
		return
	}
	if err := s.recordPlatformPrincipalDirectoryUse(user, group, installation, route, s.auditIP(r)); err != nil {
		jsonErrorCode(w, 500, "db_error", "principal directory unavailable")
		return
	}
	writeJSON(w, document)
}

func (s *Server) apiPlatformPrincipalSearch(w http.ResponseWriter, r *http.Request, user string) {
	s.platformPrincipalCall(w, r, user, "search", func(raw []byte) (platformPrincipalBinding, platformPrincipalAnswer, bool) {
		var request platformPrincipalSearchRequest
		if !strictPlatformPrincipalJSON(raw, &request) {
			return platformPrincipalBinding{}, nil, false
		}
		limit := platformPrincipalPageMax
		if request.Limit != nil {
			limit = *request.Limit
		}
		after, cursorOK := platformPrincipalAfter(request.Kind, request.After)
		if (request.Kind != "user" && request.Kind != "ou") || !cursorOK || len(request.Query) > platformPrincipalQueryMax || !utf8.ValidString(request.Query) || limit < 1 || limit > platformPrincipalPageMax {
			return platformPrincipalBinding{}, nil, false
		}
		query := strings.ToLower(request.Query)
		return request.Binding, func(directory platformPrincipalDirectory) any {
			entries := directory.users
			if request.Kind == "ou" {
				entries = directory.ous
			}
			matched := make([]platformPrincipalEntry, 0, limit+1)
			for _, entry := range entries {
				if entry.principal <= after || (query != "" && !strings.Contains(strings.ToLower(entry.label), query)) {
					continue
				}
				matched = append(matched, entry)
				if len(matched) > limit {
					break
				}
			}
			next := ""
			if len(matched) > limit {
				matched = matched[:limit]
				next = platformPrincipalCursor(matched[limit-1].principal)
			}
			if request.Kind == "ou" {
				items := make([]platformPrincipalOUItem, 0, len(matched))
				for _, entry := range matched {
					item := platformPrincipalOUItem{Principal: entry.principal, Label: entry.label}
					if entry.parent != "" {
						parent := entry.parent
						item.Parent = &parent
					}
					items = append(items, item)
				}
				return map[string]any{"items": items, "next": next}
			}
			items := make([]platformPrincipalUserItem, 0, len(matched))
			for _, entry := range matched {
				items = append(items, platformPrincipalUserItem{Principal: entry.principal, Label: entry.label})
			}
			return map[string]any{"items": items, "next": next}
		}, true
	})
}

func (s *Server) apiPlatformPrincipalResolve(w http.ResponseWriter, r *http.Request, user string) {
	s.platformPrincipalCall(w, r, user, "resolve", func(raw []byte) (platformPrincipalBinding, platformPrincipalAnswer, bool) {
		var request platformPrincipalResolveRequest
		if !strictPlatformPrincipalJSON(raw, &request) || len(request.Principals) < 1 || len(request.Principals) > platformPrincipalResolveMax {
			return platformPrincipalBinding{}, nil, false
		}
		seen := make(map[string]bool, len(request.Principals))
		for _, principal := range request.Principals {
			if !platformPrincipalPattern.MatchString(principal) || seen[principal] {
				return platformPrincipalBinding{}, nil, false
			}
			seen[principal] = true
		}
		return request.Binding, func(directory platformPrincipalDirectory) any {
			labels := make(map[string]string, len(directory.users)+len(directory.ous))
			for _, entry := range slices.Concat(directory.users, directory.ous) {
				labels[entry.principal] = entry.label
			}
			items := make([]platformPrincipalUserItem, 0, len(request.Principals))
			unknown := make([]string, 0)
			for _, principal := range request.Principals {
				if label, ok := labels[principal]; ok {
					items = append(items, platformPrincipalUserItem{Principal: principal, Label: label})
				} else {
					unknown = append(unknown, principal)
				}
			}
			return map[string]any{"items": items, "unknown": unknown}
		}, true
	})
}

// recordPlatformPrincipalDirectoryUse keeps one audit row per viewer, installation and clock hour,
// counting calls per route. Queries and results are never recorded.
func (s *Server) recordPlatformPrincipalDirectoryUse(username string, group int64, installation, route, ip string) error {
	now := time.Now().UTC()
	hour := now.Truncate(time.Hour).Format(time.RFC3339)
	const action = "app_platform.principal_directory_used"
	// One transaction per call. SQLite has a single writer connection, so the transaction is the
	// serialization; on PostgreSQL an advisory lock serializes it across processes, because
	// there is no unique key a missing hourly row could conflict on.
	tx, err := s.st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	if s.st.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780721)`); err != nil {
			return err
		}
	}
	var id int64
	var detail string
	err = e.queryRow(`SELECT id,detail FROM audit_log WHERE action=? AND actor=? AND target_type='app_platform_installation' AND target_id=? AND at>=? ORDER BY id DESC LIMIT 1`, action, username, installation, hour).Scan(&id, &detail)
	if err == nil {
		counts := map[string]any{}
		if json.Unmarshal([]byte(detail), &counts) != nil {
			counts = map[string]any{}
		}
		current, _ := counts[route].(float64)
		counts[route] = int(current) + 1
		counts["hour"] = hour
		updated, err := json.Marshal(counts)
		if err != nil {
			return err
		}
		if _, err = e.exec(`UPDATE audit_log SET detail=? WHERE id=?`, string(updated), id); err != nil {
			return err
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	first, err := json.Marshal(map[string]any{"hour": hour, route: 1})
	if err != nil {
		return err
	}
	if _, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,?)`, now.Format(time.RFC3339), username, group, action, installation, string(first), ip); err != nil {
		return err
	}
	return tx.Commit()
}
