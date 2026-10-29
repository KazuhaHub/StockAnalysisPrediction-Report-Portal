package app

import (
	"fmt"
	"strings"
	"unicode"
)

// Verification is read-only. Catalog definitions, not the migration ledger or
// constraint names alone, must still establish the approved storage contract.
func verifyAppPlatformSchema(e migExec) error {
	return verifyPlatformSchemaProducts(e, platformSchemaTables(), platformSchemaIndexes())
}

func verifyPlatformSchemaProducts(e migExec, tables []platformTable, declaredIndexes []platformIndex) error {
	if e.s.driver == "postgres" {
		return verifyPlatformPostgresProducts(e, tables, declaredIndexes)
	}
	var enabled int
	if err := e.queryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return fmt.Errorf("application control requires foreign-key enforcement")
	}
	expected := map[string]string{}
	for _, t := range tables {
		expected[platformPrefix+t.name] = compactPlatformSQL(t.ddl("sqlite"))
	}
	for _, i := range declaredIndexes {
		expected[i.name] = compactPlatformSQL(i.ddl())
	}
	rows, err := e.query(`SELECT name,sql FROM sqlite_master WHERE sql IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			return err
		}
		want, ok := expected[name]
		if !ok {
			continue
		}
		if compactPlatformSQL(definition) != want {
			return fmt.Errorf("application control product %s has an incompatible definition", name)
		}
		delete(expected, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("application control products are missing")
	}
	return nil
}

func platformTableFilter(tables []platformTable) string {
	var names []string
	for _, t := range tables {
		names = append(names, "'"+platformPrefix+t.name+"'")
	}
	return "(" + strings.Join(names, ",") + ")"
}

func verifyPlatformPostgresProducts(e migExec, tables []platformTable, declaredIndexes []platformIndex) error {
	type columnShape struct {
		typ      string
		nullable bool
	}
	columns := map[string]columnShape{}
	constraints := map[string]string{}
	constraintKinds := map[string]string{}
	constraintTargets := map[string]string{}
	for _, t := range tables {
		name := platformPrefix + t.name
		for _, c := range t.columns() {
			columns[name+"."+c.name] = columnShape{strings.ToLower(c.sqlType("postgres")), c.nullable}
		}
		constraints[name+"_pk"] = "PRIMARY KEY (" + t.primary + ")"
		constraintKinds[name+"_pk"] = "p"
		for n, r := range t.references {
			key := fmt.Sprintf("%s_fk_%d", name, n+1)
			constraints[key] = "FOREIGN KEY (" + r.columns + ") REFERENCES " + r.target + "(" + r.targetColumns + ") ON DELETE " + r.deletion + " DEFERRABLE INITIALLY DEFERRED"
			constraintKinds[key] = "f"
			constraintTargets[key] = r.target
		}
		for n, check := range t.checkExpressions("postgres") {
			key := fmt.Sprintf("%s_ck_%d", name, n+1)
			constraints[key] = check
			constraintKinds[key] = "c"
		}
	}
	rows, err := e.query(`SELECT c.relname,a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,a.atthasdef,c.relkind,c.relispartition
        FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relname IN ` + platformTableFilter(tables) + ` AND a.attnum>0 AND NOT a.attisdropped`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table, name, typ, kind string
		var required, hasDefault, partition bool
		if err := rows.Scan(&table, &name, &typ, &required, &hasDefault, &kind, &partition); err != nil {
			rows.Close()
			return err
		}
		key := table + "." + name
		want, ok := columns[key]
		if !ok || want.typ != typ || want.nullable == required || hasDefault || kind != "r" || partition {
			rows.Close()
			return fmt.Errorf("application control column %s has an incompatible definition", key)
		}
		delete(columns, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(columns) != 0 {
		return fmt.Errorf("application control columns are missing")
	}
	rows, err = e.query(`SELECT k.conname,k.contype,pg_get_constraintdef(k.oid,false),k.condeferrable,k.condeferred,k.convalidated
        FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relname IN ` + platformTableFilter(tables) + ` AND k.contype<>'n'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, kind, definition string
		var deferrable, deferred, valid bool
		if err := rows.Scan(&name, &kind, &definition, &deferrable, &deferred, &valid); err != nil {
			rows.Close()
			return err
		}
		want, ok := constraints[name]
		if !ok || constraintKinds[name] != kind || !valid || (kind == "f") != deferrable || (kind == "f") != deferred {
			rows.Close()
			return fmt.Errorf("application control constraint %s is incompatible", name)
		}
		same := compactPlatformSQL(definition) == compactPlatformSQL(want)
		if kind == "f" {
			same = compactPlatformForeignKey(definition, constraintTargets[name]) == compactPlatformForeignKey(want, constraintTargets[name])
		}
		if kind == "c" {
			same = canonicalPlatformBoolean(strings.TrimSpace(strings.TrimPrefix(definition, "CHECK"))) == canonicalPlatformBoolean(want)
		}
		if !same {
			rows.Close()
			return fmt.Errorf("application control constraint %s has an incompatible definition", name)
		}
		delete(constraints, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(constraints) != 0 {
		return fmt.Errorf("application control constraints are missing")
	}
	indexes := map[string]platformIndex{}
	for _, i := range declaredIndexes {
		indexes[i.name] = i
	}
	rows, err = e.query(`SELECT ix.relname,t.relname,i.indisunique,i.indisvalid,i.indisready,
        COALESCE((SELECT string_agg(a.attname,',' ORDER BY key.ord) FROM unnest(i.indkey) WITH ORDINALITY AS key(attnum,ord)
          JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=key.attnum),''),COALESCE(pg_get_expr(i.indpred,i.indrelid),'')
        FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid JOIN pg_class t ON t.oid=i.indrelid
        JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname='public' AND t.relname IN ` + platformTableFilter(tables) + ` AND NOT i.indisprimary`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, table, fields, predicate string
		var unique, valid, ready bool
		if err := rows.Scan(&name, &table, &unique, &valid, &ready, &fields, &predicate); err != nil {
			return err
		}
		want, ok := indexes[name]
		if !ok || !valid || !ready || want.unique != unique || platformPrefix+want.table != table || want.columns != fields || canonicalPlatformBoolean(want.predicate) != canonicalPlatformBoolean(predicate) {
			return fmt.Errorf("application control index %s is incompatible", name)
		}
		delete(indexes, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(indexes) != 0 {
		return fmt.Errorf("application control indexes are missing")
	}
	return nil
}

// Preserve literal bytes while ignoring catalog formatting and redundant SQL
// decoration. Do not erase boolean grouping: that could bless a weakened check.
func compactPlatformSQL(s string) string {
	var b strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			b.WriteByte(c)
			if quoted && i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			quoted = !quoted
			continue
		}
		if quoted {
			b.WriteByte(c)
			continue
		}
		if unicode.IsSpace(rune(c)) {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	result := b.String()
	result = stripPlatformDecoration(result, "::text")
	for _, prefix := range []string{"createtableifnotexists", "createindexifnotexists", "createuniqueindexifnotexists"} {
		if strings.HasPrefix(result, prefix) {
			result = strings.Replace(result, prefix, strings.TrimSuffix(prefix, "ifnotexists"), 1)
			break
		}
	}
	return result
}

func canonicalPlatformBoolean(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "(") && platformClosingParen(s) == len(s)-1 {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	for _, operator := range []string{"OR", "AND"} {
		parts := splitPlatformBoolean(s, operator)
		if len(parts) > 1 {
			var normalized []string
			for _, part := range parts {
				child := canonicalPlatformBoolean(part)
				prefix := operator + "["
				if strings.HasPrefix(child, prefix) && strings.HasSuffix(child, "]") {
					child = child[len(prefix) : len(child)-1]
				}
				normalized = append(normalized, child)
			}
			return operator + "[" + strings.Join(normalized, ";") + "]"
		}
	}
	return compactPlatformSQL(s)
}
func platformClosingParen(s string) int {
	depth := 0
	quoted := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			if quoted && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if s[i] == '(' {
			depth++
		}
		if s[i] == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
func splitPlatformBoolean(s, operator string) []string {
	var parts []string
	depth, start := 0, 0
	quoted := false
	identifier := func(c byte) bool {
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			if quoted && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if s[i] == '(' {
			depth++
			continue
		}
		if s[i] == ')' {
			depth--
			continue
		}
		end := i + len(operator)
		if depth == 0 && end <= len(s) && strings.EqualFold(s[i:end], operator) && (i == 0 || !identifier(s[i-1])) && (end == len(s) || !identifier(s[end])) {
			parts = append(parts, s[start:i])
			start = end
			i = end - 1
		}
	}
	return append(parts, s[start:])
}

func stripPlatformDecoration(s, decoration string) string {
	var out strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out.WriteByte(s[i])
			if quoted && i+1 < len(s) && s[i+1] == '\'' {
				out.WriteByte('\'')
				i++
				continue
			}
			quoted = !quoted
			continue
		}
		if !quoted && strings.HasPrefix(s[i:], decoration) && (i+len(decoration) == len(s) || !((s[i+len(decoration)] >= 'a' && s[i+len(decoration)] <= 'z') || (s[i+len(decoration)] >= '0' && s[i+len(decoration)] <= '9') || s[i+len(decoration)] == '_')) {
			i += len(decoration) - 1
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func compactPlatformForeignKey(definition, target string) string {
	normalized := compactPlatformSQL(definition)
	normalized = strings.Replace(normalized, "referencespublic."+target+"(", "references"+target+"(", 1)
	suffix := "ondeletenoactiondeferrableinitiallydeferred"
	if strings.HasSuffix(normalized, suffix) {
		normalized = strings.TrimSuffix(normalized, suffix) + "deferrableinitiallydeferred"
	}
	return normalized
}
