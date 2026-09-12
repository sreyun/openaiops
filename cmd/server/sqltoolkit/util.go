package sqltoolkit

import (
	"regexp"
	"strings"
	"unicode"
)

// StripCommentsAndStrings replaces string/comment contents with spaces (length-preserving
// for position-insensitive scans). Used by audit/optimize heuristics.
func StripCommentsAndStrings(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	runes := []rune(sql)
	n := len(runes)
	for i := 0; i < n; {
		// line comment --
		if i+1 < n && runes[i] == '-' && runes[i+1] == '-' {
			for i < n && runes[i] != '\n' {
				b.WriteByte(' ')
				i++
			}
			continue
		}
		// line comment #
		if runes[i] == '#' {
			for i < n && runes[i] != '\n' {
				b.WriteByte(' ')
				i++
			}
			continue
		}
		// block comment /* */
		if i+1 < n && runes[i] == '/' && runes[i+1] == '*' {
			b.WriteByte(' ')
			b.WriteByte(' ')
			i += 2
			for i+1 < n && !(runes[i] == '*' && runes[i+1] == '/') {
				if runes[i] == '\n' {
					b.WriteRune('\n')
				} else {
					b.WriteByte(' ')
				}
				i++
			}
			if i+1 < n {
				b.WriteByte(' ')
				b.WriteByte(' ')
				i += 2
			}
			continue
		}
		// single-quoted string
		if runes[i] == '\'' {
			b.WriteByte(' ')
			i++
			for i < n {
				if runes[i] == '\\' && i+1 < n {
					b.WriteByte(' ')
					b.WriteByte(' ')
					i += 2
					continue
				}
				if runes[i] == '\'' {
					b.WriteByte(' ')
					i++
					if i < n && runes[i] == '\'' { // escaped ''
						b.WriteByte(' ')
						i++
						continue
					}
					break
				}
				b.WriteByte(' ')
				i++
			}
			continue
		}
		// double-quoted / backtick identifier — keep backticks content as X for word scans
		if runes[i] == '"' || runes[i] == '`' {
			q := runes[i]
			b.WriteByte(' ')
			i++
			for i < n {
				if runes[i] == q {
					b.WriteByte(' ')
					i++
					break
				}
				b.WriteByte('x')
				i++
			}
			continue
		}
		b.WriteRune(runes[i])
		i++
	}
	return b.String()
}

func compactSpaces(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

var mysqlKeywords = map[string]bool{
	"select": true, "from": true, "where": true, "and": true, "or": true, "not": true,
	"insert": true, "into": true, "values": true, "update": true, "set": true, "delete": true,
	"join": true, "inner": true, "left": true, "right": true, "outer": true, "cross": true,
	"on": true, "as": true, "group": true, "by": true, "order": true, "having": true,
	"limit": true, "offset": true, "union": true, "all": true, "distinct": true,
	"case": true, "when": true, "then": true, "else": true, "end": true,
	"create": true, "table": true, "index": true, "drop": true, "alter": true,
	"explain": true, "with": true, "recursive": true, "using": true, "exists": true,
	"in": true, "is": true, "null": true, "like": true, "between": true, "asc": true, "desc": true,
	"force": true, "use": true, "ignore": true, "key": true, "for": true, "share": true,
	"lock": true, "mode": true, "partition": true, "over": true, "partitioned": true,
	"row_number": true, "rank": true, "dense_rank": true, "window": true,
}

func isKeyword(w string) bool {
	return mysqlKeywords[strings.ToLower(w)]
}

// FirstKeyword returns the first SQL keyword ignoring leading comments/whitespace.
func FirstKeyword(sql string) string {
	s := strings.TrimSpace(StripCommentsAndStrings(sql))
	s = compactSpaces(s)
	if s == "" {
		return ""
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return ""
	}
	return strings.ToLower(parts[0])
}

// IsReadOnlyQuery reports whether sql is a SELECT/WITH/EXPLAIN (single statement).
func IsReadOnlyQuery(sql string) bool {
	if strings.Contains(sql, ";") {
		// allow trailing semicolon only
		trimmed := strings.TrimSpace(sql)
		if strings.Count(trimmed, ";") > 1 || !strings.HasSuffix(trimmed, ";") {
			return false
		}
		trimmed = strings.TrimSuffix(trimmed, ";")
		sql = trimmed
	}
	kw := FirstKeyword(sql)
	switch kw {
	case "select", "with", "explain", "show", "desc", "describe", "values", "table":
		return true
	default:
		return false
	}
}

// ForbiddenWrite reports DDL/DML/file ops that must never hit the connection.
func ForbiddenWrite(sql string) bool {
	s := strings.ToLower(compactSpaces(StripCommentsAndStrings(sql)))
	bad := []string{
		" insert ", " update ", " delete ", " drop ", " alter ", " create ",
		" truncate ", " replace ", " grant ", " revoke ", " rename ",
		" into outfile", " into dumpfile", " load data", " call ",
		" lock tables", " unlock tables", " set global", " set @@",
		" copy ", " \\copy ", " execute ", " prepare ", " deallocate ",
		" do ", " listen ", " notify ", " vacuum ", " reindex ",
		" pg_sleep(", " sleep(", " benchmark(", " get_lock(",
	}
	padded := " " + s + " "
	for _, b := range bad {
		if strings.Contains(padded, b) {
			return true
		}
	}
	// SELECT-shaped wrappers that still execute arbitrary SQL / mutate state.
	// Matched with optional whitespace before '(' and optional schema qualifier
	// (pg_catalog.query_to_xml / public.dblink_exec).
	for _, fn := range mutatingSelectFuncs {
		if containsFuncCall(s, fn) {
			return true
		}
	}
	kw := FirstKeyword(sql)
	switch kw {
	case "insert", "update", "delete", "drop", "alter", "create", "truncate", "replace", "grant", "revoke", "call", "load",
		"copy", "execute", "prepare", "do", "vacuum", "reindex":
		return true
	}
	return false
}

// mutatingSelectFuncs look like read-only SELECT calls but execute attacker-chosen
// SQL or mutate server state. Used by ForbiddenWrite and StrictReadOnly*.
var mutatingSelectFuncs = []string{
	"query_to_xml", // runs the query text argument
	"dblink_exec",  // executes a command string
	"dblink",       // can run any SQL on the linked connection
	"setval",       // mutates sequence state
}

// containsFuncCall reports whether flat (already lowercased / comment-stripped /
// space-compacted) contains a call to funcName, allowing an optional schema
// qualifier and optional whitespace before '('.
func containsFuncCall(flat, funcName string) bool {
	name := strings.ToLower(funcName)
	flat = strings.ToLower(flat)
	idx := 0
	for {
		i := strings.Index(flat[idx:], name)
		if i < 0 {
			return false
		}
		i += idx
		if i > 0 {
			prev := flat[i-1]
			// Allow schema.func; reject identifier prefixes (my_query_to_xml).
			if isIdentByte(prev) {
				idx = i + len(name)
				continue
			}
		}
		after := i + len(name)
		for after < len(flat) && flat[after] == ' ' {
			after++
		}
		if after < len(flat) && flat[after] == '(' {
			return true
		}
		idx = i + len(name)
		if idx >= len(flat) {
			return false
		}
	}
}

// IsAllowedIndexDDL reports whether sql is a single, narrowly-scoped index DDL
// (CREATE [UNIQUE] INDEX / ALTER TABLE … ADD [UNIQUE] INDEX|KEY). Used by the
// controlled exec-ddl API — everything else (DROP, DML, CREATE TABLE, …) is rejected.
func IsAllowedIndexDDL(sql string) bool {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return false
	}
	// Single statement only (allow one trailing semicolon).
	if strings.Count(trimmed, ";") > 1 || (strings.Contains(trimmed, ";") && !strings.HasSuffix(trimmed, ";")) {
		return false
	}
	trimmed = strings.TrimSuffix(trimmed, ";")
	s := strings.ToLower(compactSpaces(StripCommentsAndStrings(trimmed)))
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, ";") {
		return false
	}
	// Hard reject destructive / unrelated keywords even if somehow nested.
	for _, bad := range []string{
		" drop ", " truncate ", " delete ", " insert ", " update ", " grant ", " revoke ",
		" create table", " create database", " create view", " create procedure", " create function",
		" create trigger", " create event", " rename ", " into outfile", " load data", " call ",
		" add column", " modify column", " change column", " partition ", " disable keys", " enable keys",
		" add constraint", " add foreign", " add primary", " add fulltext", " add spatial",
	} {
		if strings.Contains(" "+s+" ", bad) || strings.HasPrefix(s, strings.TrimSpace(bad)) {
			return false
		}
	}
	// Reject multi-clause ALTER (e.g. ADD INDEX …, ADD COLUMN …).
	if strings.HasPrefix(s, "alter ") && strings.Contains(s, ",") {
		return false
	}
	// Full-statement match — prefix-only regex previously allowed trailing clauses.
	// Identifiers: letters/digits/_/$/dot; optional MySQL-style `quoted` names.
	ident := `[a-z0-9_$.]+|` + "`" + `[a-z0-9_$.]+` + "`"
	reCreate := regexp.MustCompile(`^create\s+(unique\s+)?index\s+(?:` + ident + `)\s+on\s+(?:` + ident + `)\s*\([^)]+\)$`)
	reAlter := regexp.MustCompile(`^alter\s+table\s+(?:` + ident + `)\s+add\s+(unique\s+)?(index|key)\s+(?:` + ident + `)\s*\([^)]+\)$`)
	return reCreate.MatchString(s) || reAlter.MatchString(s)
}
