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

// dangerousSQLFuncs are side-effecting / host-filesystem helpers that must never
// run through the read-only SQL workbench, even when wrapped in SELECT.
// Matching uses containsFuncCall so "load_file ('…')" (space before '(') still hits.
var dangerousSQLFuncs = []string{
	"pg_sleep", "sleep", "benchmark", "get_lock", "load_file",
	"set_config", "lo_export", "lo_import",
	// PostgreSQL admin file helpers — SELECT-callable, not blocked by READ ONLY txns.
	"pg_read_file", "pg_read_binary_file", "pg_ls_dir", "pg_stat_file",
}

// containsFuncCall reports whether flat (lowercased, compactSpaces'd SQL without
// string/comment literals) contains an identifier call to name with optional
// whitespace before '('. Identifier boundaries prevent matching substrings
// (e.g. my_load_file).
func containsFuncCall(flat, name string) bool {
	if flat == "" || name == "" {
		return false
	}
	idx := 0
	for {
		i := strings.Index(flat[idx:], name)
		if i < 0 {
			return false
		}
		i += idx
		beforeOK := i == 0 || !isIdentByte(flat[i-1])
		after := i + len(name)
		if !beforeOK {
			idx = after
			continue
		}
		j := after
		for j < len(flat) && flat[j] == ' ' {
			j++
		}
		if j < len(flat) && flat[j] == '(' {
			return true
		}
		idx = after
		if idx >= len(flat) {
			return false
		}
	}
}

func containsDangerousSQLFunc(flat string) string {
	for _, name := range dangerousSQLFuncs {
		if containsFuncCall(flat, name) {
			return name
		}
	}
	return ""
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
	}
	padded := " " + s + " "
	for _, b := range bad {
		if strings.Contains(padded, b) {
			return true
		}
	}
	if containsDangerousSQLFunc(s) != "" {
		return true
	}
	kw := FirstKeyword(sql)
	switch kw {
	case "insert", "update", "delete", "drop", "alter", "create", "truncate", "replace", "grant", "revoke", "call", "load",
		"copy", "execute", "prepare", "do", "vacuum", "reindex":
		return true
	}
	return false
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
