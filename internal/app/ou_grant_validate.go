package app

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// errOUPrincipalMissing means a grant named an OU that is not in the tree. The admin API validates
// its principals before calling a writer, so reaching this from a request means the OU was deleted
// in between: a conflict for the caller to retry, not a server fault.
var errOUPrincipalMissing = errors.New("grant names an OU that no longer exists")

// validateOUPrincipals is the one place a principal string is checked before it is stored as an
// access relation. It runs INSIDE the OU writer boundary, so the tree cannot change between the
// check and the insert: an OU deleted concurrently is either seen here (and refused) or waits for
// this transaction to commit before it can delete.
//
// A group principal must name an OU that exists. An account principal is NOT validated against the
// accounts table on purpose: a run outlives its submitter, a token can name a person whose account
// was removed while the run executed, and a historical attribution row legitimately names someone
// who is gone. Account lifecycles are handled by DeleteUser's own sweep, not by this check.
//
// Duplicates and blanks are folded exactly as they always were, so a save that has always succeeded
// still succeeds and stores the same list. A read that FAILS is reported as itself, never as a
// missing OU: the caller must not mistake a broken database for a deleted one.
func validateOUPrincipals(e migExec, principals []string) ([]string, error) {
	out := make([]string, 0, len(principals))
	seen := map[string]bool{}
	for _, raw := range principals {
		p := strings.TrimSpace(raw)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if strings.HasPrefix(p, "g:") {
			id, err := strconv.ParseInt(strings.TrimPrefix(p, "g:"), 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("%w: %q", errOUPrincipalMissing, p)
			}
			var present int
			switch err := e.queryRow(`SELECT 1 FROM user_groups WHERE id=?`, id).Scan(&present); {
			case errors.Is(err, sql.ErrNoRows):
				return nil, fmt.Errorf("%w: %q", errOUPrincipalMissing, p)
			case err != nil:
				return nil, err
			}
			p = groupPrincipal(id)
		}
		out = append(out, p)
	}
	return out, nil
}

// writeGrantWriteError answers a failed grant save. A missing OU is a conflict — the tree moved
// under the request — so the admin can retry against the reloaded list instead of reading it as a
// server fault. Everything else keeps the envelope these handlers always used.
func writeGrantWriteError(w http.ResponseWriter, err error) {
	if errors.Is(err, errOUPrincipalMissing) {
		jsonErrorCode(w, http.StatusConflict, "ou_missing", "a granted OU no longer exists; reload and try again")
		return
	}
	jsonError(w, http.StatusInternalServerError, err.Error())
}
