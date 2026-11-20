package journal

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// The quota evidence of a namespace is a chain. The install's prepare-data receipt is its first link and is
// immutable: its payload digest is rebuilt from it by every later checkpoint. Each applied set-data-quota row is
// the next link, raising the limits from exactly where the previous link left them (doc 185 section 3.4). The
// current quota is the last link; the quota a start recorded at its own time is any link.

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// quotaLink is one point of the chain.
type quotaLink struct{ Bytes, Inodes int64 }

// loadQuotaChain returns the chain of a namespace, oldest first, starting with the install's limits. A raise that
// does not start where the previous link ended, or does not grow, is a corrupt chain and is refused.
func loadQuotaChain(ctx context.Context, q queryer, namespaceID string, installBytes, installInodes string) ([]quotaLink, error) {
	bytes, err1 := strconv.ParseInt(installBytes, 10, 64)
	inodes, err2 := strconv.ParseInt(installInodes, 10, 64)
	if err1 != nil || err2 != nil || bytes <= 0 || inodes <= 0 {
		return nil, fmt.Errorf("%w: install quota", ErrFenceReconciliation)
	}
	chain := []quotaLink{{bytes, inodes}}
	rows, err := q.QueryContext(ctx, `SELECT receipt_json FROM runner_commands WHERE kind='set-data-quota' AND state='applied' ORDER BY rowid LIMIT 4097`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 4096 {
			return nil, fmt.Errorf("%w: quota chain too long", ErrFenceReconciliation)
		}
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		receipt, err := ParseSetDataQuotaReceipt([]byte(raw))
		if err != nil {
			return nil, err
		}
		if receipt.NamespaceID != namespaceID {
			continue
		}
		last := chain[len(chain)-1]
		oldBytes, oldInodes := mustDataInt(receipt.OldBytes), mustDataInt(receipt.OldInodes)
		newBytes, newInodes := mustDataInt(receipt.NewBytes), mustDataInt(receipt.NewInodes)
		if oldBytes != last.Bytes || oldInodes != last.Inodes || newBytes < oldBytes || newInodes < oldInodes {
			return nil, fmt.Errorf("%w: quota chain is broken", ErrFenceReconciliation)
		}
		chain = append(chain, quotaLink{newBytes, newInodes})
	}
	return chain, rows.Err()
}

// currentQuota is the last link of the chain, as the decimal strings every command carries.
func currentQuota(ctx context.Context, q queryer, namespaceID, installBytes, installInodes string) (string, string, error) {
	chain, err := loadQuotaChain(ctx, q, namespaceID, installBytes, installInodes)
	if err != nil {
		return "", "", err
	}
	last := chain[len(chain)-1]
	return strconv.FormatInt(last.Bytes, 10), strconv.FormatInt(last.Inodes, 10), nil
}

// quotaInChain reports whether the limits are a link of the chain: the quota a start recorded at its own time may
// be any link, because a raise can come before or after it.
func quotaInChain(chain []quotaLink, bytes, inodes string) bool {
	b, err1 := strconv.ParseInt(bytes, 10, 64)
	i, err2 := strconv.ParseInt(inodes, 10, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	for _, link := range chain {
		if link.Bytes == b && link.Inodes == i {
			return true
		}
	}
	return false
}
