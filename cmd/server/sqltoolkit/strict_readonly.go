package sqltoolkit

import (
	"strings"
)

// StrictReadOnlyMySQL returns a non-empty rejection reason when sql is not a
// safe read-only MySQL workbench statement. Empty string means allowed.
func StrictReadOnlyMySQL(sql string) string {
	return strictReadOnly(sql, true)
}

// StrictReadOnlyPostgres returns a non-empty rejection reason when sql is not a
// safe read-only PostgreSQL workbench statement.
func StrictReadOnlyPostgres(sql string) string {
	return strictReadOnly(sql, false)
}

func strictReadOnly(sql string, mysql bool) string {
	raw := strings.TrimSpace(sql)
	if raw == "" {
		return "empty SQL"
	}
	// Single statement only (one trailing semicolon allowed).
	trimmed := raw
	if strings.Contains(trimmed, ";") {
		if strings.Count(trimmed, ";") > 1 || !strings.HasSuffix(trimmed, ";") {
			return "multiple statements"
		}
		trimmed = strings.TrimSuffix(trimmed, ";")
	}
	if !IsReadOnlyQuery(trimmed+";") && !IsReadOnlyQuery(trimmed) {
		return "not a read-only statement"
	}
	kw := FirstKeyword(trimmed)
	switch kw {
	case "select", "with", "show", "desc", "describe", "values", "table", "explain":
		// ok
	default:
		return "keyword not allowed: " + kw
	}
	flat := strings.ToLower(compactSpaces(StripCommentsAndStrings(trimmed)))
	padded := " " + flat + " "

	// Locking / mutating SELECT clauses.
	for _, bad := range []string{
		" for update", " for share", " lock in share mode",
		" into outfile", " into dumpfile", " into ",
	} {
		if strings.Contains(padded, bad) {
			// Allow "INTO" only when it's not SELECT … INTO (already caught) —
			// "select * into new_table" contains " into ".
			if bad == " into " {
				if strings.Contains(padded, " into outfile") || strings.Contains(padded, " into dumpfile") ||
					strings.Contains(flat, "select") && strings.Contains(flat, " into ") {
					return "forbidden clause: INTO"
				}
				continue
			}
			return "forbidden clause:" + bad
		}
	}
	// Dangerous functions / session writes (tolerate whitespace before '(').
	if name := containsDangerousSQLFunc(flat); name != "" {
		return "forbidden function: " + name
	}
	if strings.HasPrefix(flat, "set ") {
		return "SET not allowed"
	}
	if strings.HasPrefix(flat, "copy ") || strings.Contains(padded, " copy ") {
		return "COPY not allowed"
	}
	// CTE that embeds DML.
	if kw == "with" || kw == "select" {
		for _, w := range []string{"delete", "insert", "update", "drop", "truncate"} {
			if containsSQLKeyword(flat, w) {
				return "mutating statement: " + w
			}
		}
	}
	if ForbiddenWrite(trimmed) {
		// Re-check: ForbiddenWrite is broad; SHOW/DESC/VALUES may contain substrings.
		if kw == "show" || kw == "desc" || kw == "describe" || kw == "values" || kw == "table" || kw == "explain" {
			// still scan for explicit DML keywords as first CTE body etc.
			for _, w := range []string{" insert ", " update ", " delete ", " drop ", " alter ", " create ", " truncate ", " copy "} {
				if strings.Contains(padded, w) {
					return "write keyword"
				}
			}
		} else if kw == "select" || kw == "with" {
			for _, w := range []string{
				" insert ", " update ", " delete ", " drop ", " alter ", " create ",
				" truncate ", " replace ", " grant ", " revoke ",
				" into outfile", " into dumpfile", " load data",
			} {
				if strings.Contains(padded, w) {
					return "forbidden pattern"
				}
			}
			if containsDangerousSQLFunc(flat) != "" {
				return "forbidden pattern"
			}
		} else {
			return "write statement"
		}
	}
	_ = mysql
	return ""
}

func containsSQLKeyword(flat, kw string) bool {
	flat = strings.ToLower(flat)
	kw = strings.ToLower(kw)
	idx := 0
	for {
		i := strings.Index(flat[idx:], kw)
		if i < 0 {
			return false
		}
		i += idx
		beforeOK := i == 0 || !isIdentByte(flat[i-1])
		after := i + len(kw)
		afterOK := after >= len(flat) || !isIdentByte(flat[after])
		if beforeOK && afterOK {
			return true
		}
		idx = i + len(kw)
		if idx >= len(flat) {
			return false
		}
	}
}

func isIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}
