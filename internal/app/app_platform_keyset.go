package app

import (
	"context"
	"database/sql"
	"sync"
)

// platformKeysetCursor remembers the key at which the last window of a settlement driver ended.
//
// The drivers that settle finished operations read their candidates in a window of a few rows per tick. A window that
// always began at the oldest row would be taken up for good by candidates that keep failing (a runner that stays
// unreachable, evidence that no longer verifies), and the ones after them would never be reached. The window therefore
// walks the candidates in key order, starting after the key the previous window ended at and wrapping to the start of
// the key space when it runs out. It is a keyset, not an offset: a row that leaves the set (settled, or removed, the
// row the cursor stands on included) cannot make the walk skip or repeat another, and the walk covers the whole set
// however large it is. The cursor lives in memory; a restart begins again at the first key, which only repeats work.
type platformKeysetCursor struct {
	mu   sync.Mutex
	last string
}

// platformKeysetWindow reads up to window rows. selectFrom is the SELECT and joins, where is the filter (without the
// WHERE), key is the unique, stable column the rows are ordered by, and scan returns a row, its key, and whether to keep
// it (a row that is skipped still advances the cursor, so an invalid row cannot hold the window).
func platformKeysetWindow[T any](ctx context.Context, st *Store, c *platformKeysetCursor, selectFrom, where, key string, args []any, window int, scan func(*sql.Rows) (T, string, bool, error)) ([]T, error) {
	c.mu.Lock()
	last := c.last
	c.mu.Unlock()
	var out []T
	end := last
	read := func(cmp string, limit int) error {
		q := selectFrom + " WHERE (" + where + ") AND " + key + " " + cmp + " ? ORDER BY " + key + " LIMIT ?"
		rows, err := st.queryContext(ctx, q, append(append([]any{}, args...), last, limit)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, k, keep, err := scan(rows)
			if err != nil {
				return err
			}
			end = k
			if keep {
				out = append(out, item)
			}
		}
		return rows.Err()
	}
	if err := read(">", window); err != nil {
		return nil, err
	}
	if remaining := window - len(out); remaining > 0 && last != "" {
		// The end of the key space: continue from the first key, up to the key the walk started after.
		if err := read("<=", remaining); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	c.last = end
	c.mu.Unlock()
	return out, nil
}
