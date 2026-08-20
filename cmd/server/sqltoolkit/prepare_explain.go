package sqltoolkit

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	reFromQuotedIdent   = regexp.MustCompile(`(?i)\b(from|join|update|into|table)\s+'[A-Za-z_][A-Za-z0-9_]*'`)
	reSimpleQuotedIdent = regexp.MustCompile(`^'[A-Za-z_][A-Za-z0-9_]*'$`)
	reQuotedFuncCall    = regexp.MustCompile("(?i)([`'\"]+)([A-Za-z_][A-Za-z0-9_]*)([`'\"]+)\\s*\\(")
	reCountStarSpaced   = regexp.MustCompile(`(?i)\bCOUNT\s*\(\s*\*\s*\)`)
	reDateFormatNullEq  = regexp.MustCompile(`(?i)\bDATE_FORMAT\s*\(\s*([^,]+?)\s*,\s*NULL\s*\)\s*=\s*NULL`)
	reDateFormatNullArg = regexp.MustCompile(`(?i)\bDATE_FORMAT\s*\(\s*([^,]+?)\s*,\s*NULL\s*\)`)
	reDateFormatEqNull  = regexp.MustCompile(`(?i)(\bDATE_FORMAT\s*\([^)]+\))\s*=\s*NULL`)
	reStrToDateNullArg  = regexp.MustCompile(`(?i)\bSTR_TO_DATE\s*\(\s*([^,]+?)\s*,\s*NULL\s*\)`)
	reFromUnixNullArg   = regexp.MustCompile(`(?i)\bFROM_UNIXTIME\s*\(\s*NULL\s*\)`)
)

// MySQL / PG built-ins that must not be identifier-quoted when used as func(...).
// Digest / ORM dumps often emit `DATE_FORMAT` (...) which MySQL rejects as syntax.
var sqlBuiltinFuncs = map[string]struct{}{
	"ABS": {}, "ACOS": {}, "ADDDATE": {}, "ADDTIME": {}, "AES_DECRYPT": {}, "AES_ENCRYPT": {},
	"ASCII": {}, "ASIN": {}, "ATAN": {}, "ATAN2": {}, "AVG": {}, "BENCHMARK": {}, "BIN": {},
	"BINARY": {}, "BIT_AND": {}, "BIT_COUNT": {}, "BIT_LENGTH": {}, "BIT_OR": {}, "BIT_XOR": {},
	"CEIL": {}, "CEILING": {}, "CHAR": {}, "CHAR_LENGTH": {}, "CHARACTER_LENGTH": {}, "CHARSET": {},
	"COALESCE": {}, "COERCIBILITY": {}, "COLLATION": {}, "COMPRESS": {}, "CONCAT": {}, "CONCAT_WS": {},
	"CONNECTION_ID": {}, "CONV": {}, "CONVERT": {}, "CONVERT_TZ": {}, "COS": {}, "COT": {},
	"COUNT": {}, "CRC32": {}, "CURDATE": {}, "CURRENT_DATE": {}, "CURRENT_TIME": {}, "CURRENT_TIMESTAMP": {},
	"CURRENT_USER": {}, "CURTIME": {}, "DATABASE": {}, "DATE": {}, "DATE_ADD": {}, "DATE_FORMAT": {},
	"DATE_SUB": {}, "DATEDIFF": {}, "DAY": {}, "DAYNAME": {}, "DAYOFMONTH": {}, "DAYOFWEEK": {},
	"DAYOFYEAR": {}, "DECODE": {}, "DEFAULT": {}, "DEGREES": {}, "DES_DECRYPT": {}, "DES_ENCRYPT": {},
	"ELT": {}, "ENCODE": {}, "ENCRYPT": {}, "EXP": {}, "EXPORT_SET": {}, "EXTRACT": {}, "FIELD": {},
	"FIND_IN_SET": {}, "FLOOR": {}, "FORMAT": {}, "FOUND_ROWS": {}, "FROM_BASE64": {}, "FROM_DAYS": {},
	"FROM_UNIXTIME": {}, "GET_FORMAT": {}, "GET_LOCK": {}, "GREATEST": {}, "GROUP_CONCAT": {},
	"HEX": {}, "HOUR": {}, "IF": {}, "IFNULL": {}, "INET_ATON": {}, "INET_NTOA": {}, "INSERT": {},
	"INSTR": {}, "INTERVAL": {}, "IS_FREE_LOCK": {}, "IS_IPV4": {}, "IS_IPV6": {}, "IS_USED_LOCK": {},
	"ISNULL": {}, "JSON_ARRAY": {}, "JSON_ARRAYAGG": {}, "JSON_EXTRACT": {}, "JSON_OBJECT": {},
	"JSON_OBJECTAGG": {}, "JSON_UNQUOTE": {}, "LAST_DAY": {}, "LAST_INSERT_ID": {}, "LCASE": {},
	"LEAST": {}, "LEFT": {}, "LENGTH": {}, "LN": {}, "LOAD_FILE": {}, "LOCALTIME": {}, "LOCALTIMESTAMP": {},
	"LOCATE": {}, "LOG": {}, "LOG10": {}, "LOG2": {}, "LOWER": {}, "LPAD": {}, "LTRIM": {}, "MAKE_SET": {},
	"MAKEDATE": {}, "MAKETIME": {}, "MASTER_POS_WAIT": {}, "MAX": {}, "MD5": {}, "MICROSECOND": {},
	"MID": {}, "MIN": {}, "MINUTE": {}, "MOD": {}, "MONTH": {}, "MONTHNAME": {}, "NAME_CONST": {},
	"NOW": {}, "NULLIF": {}, "OCT": {}, "OCTET_LENGTH": {}, "OLD_PASSWORD": {}, "ORD": {},
	"PASSWORD": {}, "PERIOD_ADD": {}, "PERIOD_DIFF": {}, "PI": {}, "POSITION": {}, "POW": {}, "POWER": {},
	"QUARTER": {}, "QUOTE": {}, "RADIANS": {}, "RAND": {}, "RELEASE_LOCK": {}, "REPEAT": {},
	"REPLACE": {}, "REVERSE": {}, "RIGHT": {}, "ROUND": {}, "ROW_COUNT": {}, "RPAD": {}, "RTRIM": {},
	"SCHEMA": {}, "SEC_TO_TIME": {}, "SECOND": {}, "SESSION_USER": {}, "SHA": {}, "SHA1": {}, "SHA2": {},
	"SIGN": {}, "SIN": {}, "SLEEP": {}, "SOUNDEX": {}, "SPACE": {}, "SQRT": {}, "STD": {}, "STDDEV": {},
	"STDDEV_POP": {}, "STDDEV_SAMP": {}, "STR_TO_DATE": {}, "STRCMP": {}, "SUBDATE": {}, "SUBSTR": {},
	"SUBSTRING": {}, "SUBSTRING_INDEX": {}, "SUBTIME": {}, "SUM": {}, "SYSDATE": {}, "SYSTEM_USER": {},
	"TAN": {}, "TIME": {}, "TIME_FORMAT": {}, "TIME_TO_SEC": {}, "TIMEDIFF": {}, "TIMESTAMP": {},
	"TIMESTAMPADD": {}, "TIMESTAMPDIFF": {}, "TO_BASE64": {}, "TO_DAYS": {}, "TO_SECONDS": {},
	"TRIM": {}, "TRUNCATE": {}, "UCASE": {}, "UNCOMPRESS": {}, "UNCOMPRESSED_LENGTH": {}, "UNHEX": {},
	"UNIX_TIMESTAMP": {}, "UPPER": {}, "USER": {}, "UTC_DATE": {}, "UTC_TIME": {}, "UTC_TIMESTAMP": {},
	"UUID": {}, "UUID_SHORT": {}, "VALUES": {}, "VAR_POP": {}, "VAR_SAMP": {}, "VARIANCE": {},
	"VERSION": {}, "WEEK": {}, "WEEKDAY": {}, "WEEKOFYEAR": {}, "WEIGHT_STRING": {}, "YEAR": {},
	"YEARWEEK": {},
	"CAST":     {}, // CAST (x AS …) with a space → Error 1630 same as SUM (
	// Postgres-only extras (shared names already listed above)
	"TO_CHAR": {}, "TO_DATE": {}, "TO_TIMESTAMP": {}, "DATE_TRUNC": {}, "AGE": {},
	"ARRAY_AGG": {}, "STRING_AGG": {}, "JSON_AGG": {}, "JSONB_AGG": {},
	"ROW_NUMBER": {}, "RANK": {}, "DENSE_RANK": {}, "LAG": {}, "LEAD": {},
	"NTILE": {}, "FIRST_VALUE": {}, "LAST_VALUE": {}, "NTH_VALUE": {},
	"CUME_DIST": {}, "PERCENT_RANK": {}, "FILTER": {},
}

// PrepareSQLForExplain normalizes digest / prepared-statement SQL so EXPLAIN can run:
//  1. strip illegal quotes around built-in function names (`DATE_FORMAT` (...))
//  2. remove spaces between built-in names and '(' (SUM (x) → SUM(x); MySQL Error 1630)
//  3. normalize COUNT ( * ) → COUNT(*)
//  4. misquoted identifiers ('table' / 'col') → proper quoting
//  5. unbound placeholders (?, $1) → context-aware probe literals
func PrepareSQLForExplain(sql string, d Dialect) (prepared string, notes []string) {
	prepared = strings.TrimSpace(sql)
	if prepared == "" {
		return "", nil
	}
	if fixed, ok := unquoteBuiltinFunctionCalls(prepared); ok {
		prepared = fixed
		notes = append(notes, "已去除内置函数名上的非法引号（如 DATE_FORMAT）")
	}
	if fixed, ok := tightenBuiltinFuncCalls(prepared); ok {
		prepared = fixed
		notes = append(notes, "已去掉内置函数名与括号之间的空格（避免 MySQL Error 1630）")
	}
	if fixed := reCountStarSpaced.ReplaceAllString(prepared, "COUNT(*)"); fixed != prepared {
		prepared = fixed
		notes = append(notes, "已规范化 COUNT(*) 写法")
	}
	if looksLikeMisquotedIdentSQL(prepared) {
		fixed := rewriteMisquotedIdents(prepared, d)
		if fixed != prepared {
			prepared = fixed
			notes = append(notes, "已将单引号包裹的标识符规范为合法引用")
		}
	}
	if sqlHasUnboundPlaceholder(prepared) {
		fixed := substituteExplainPlaceholders(prepared)
		if fixed != prepared {
			prepared = fixed
			notes = append(notes, "已将参数占位符替换为探测值以便 EXPLAIN")
		}
	}
	if fixed, ok := SubstituteDigestQuotedPlaceholders(prepared); ok {
		prepared = fixed
		notes = append(notes, "已将 DIGEST 摘要中的 '?' 字面量替换为 NULL 探测值以便 EXPLAIN")
	}
	if fixed, ok := refineDateProbeLiterals(prepared); ok {
		prepared = fixed
		notes = append(notes, "已为日期函数补齐格式/比较探测值")
	}
	return prepared, notes
}

// HasUnboundPlaceholder reports whether SQL still contains ? / $n outside literals.
func HasUnboundPlaceholder(sql string) bool {
	return sqlHasUnboundPlaceholder(sql)
}

func unquoteBuiltinFunctionCalls(sql string) (string, bool) {
	changed := false
	out := reQuotedFuncCall.ReplaceAllStringFunc(sql, func(m string) string {
		sub := reQuotedFuncCall.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		name := strings.ToUpper(sub[2])
		if _, ok := sqlBuiltinFuncs[name]; !ok {
			return m
		}
		changed = true
		return name + "("
	})
	return out, changed
}

// tightenBuiltinFuncCalls removes whitespace between known built-in names and '('.
// MySQL treats "SUM (x)" as an identifier lookup (Error 1630: FUNCTION db.SUM does not exist)
// when IGNORE_SPACE is off (default). Digest text from performance_schema often has these spaces.
func tightenBuiltinFuncCalls(sql string) (string, bool) {
	runes := []rune(sql)
	n := len(runes)
	if n == 0 {
		return sql, false
	}
	var b strings.Builder
	b.Grow(n)
	changed := false
	i := 0
	copySpan := func(from, to int) {
		for ; from < to; from++ {
			b.WriteRune(runes[from])
		}
	}
	for i < n {
		r := runes[i]
		switch {
		case r == '-' && i+1 < n && runes[i+1] == '-':
			start := i
			for i < n && runes[i] != '\n' {
				i++
			}
			copySpan(start, i)
		case r == '#':
			start := i
			for i < n && runes[i] != '\n' {
				i++
			}
			copySpan(start, i)
		case r == '/' && i+1 < n && runes[i+1] == '*':
			start := i
			i += 2
			for i+1 < n && !(runes[i] == '*' && runes[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			}
			copySpan(start, i)
		case r == '\'' || r == '"' || r == '`':
			q := r
			start := i
			i++
			for i < n {
				if runes[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if runes[i] == q {
					i++
					if q == '\'' && i < n && runes[i] == '\'' {
						i++
						continue
					}
					break
				}
				i++
			}
			copySpan(start, i)
		case unicode.IsLetter(r) || r == '_':
			j := i + 1
			for j < n && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
				j++
			}
			name := string(runes[i:j])
			k := j
			for k < n && unicode.IsSpace(runes[k]) {
				k++
			}
			if k < n && runes[k] == '(' && k > j {
				if _, ok := sqlBuiltinFuncs[strings.ToUpper(name)]; ok {
					b.WriteString(name)
					b.WriteByte('(')
					changed = true
					i = k + 1
					continue
				}
			}
			b.WriteString(name)
			i = j
		default:
			b.WriteRune(r)
			i++
		}
	}
	if !changed {
		return sql, false
	}
	return b.String(), true
}

func refineDateProbeLiterals(sql string) (string, bool) {
	orig := sql
	sql = reDateFormatNullEq.ReplaceAllString(sql, "DATE_FORMAT($1, '%Y-%m-%d') = '1970-01-01'")
	sql = reDateFormatNullArg.ReplaceAllString(sql, "DATE_FORMAT($1, '%Y-%m-%d')")
	sql = reDateFormatEqNull.ReplaceAllString(sql, "$1 = '1970-01-01'")
	sql = reStrToDateNullArg.ReplaceAllString(sql, "STR_TO_DATE($1, '%Y-%m-%d')")
	sql = reFromUnixNullArg.ReplaceAllString(sql, "FROM_UNIXTIME(0)")
	return sql, sql != orig
}

func looksLikeMisquotedIdentSQL(sql string) bool {
	return reFromQuotedIdent.MatchString(sql)
}

func rewriteMisquotedIdents(sql string, d Dialect) string {
	tokens := tokenizeSQL(sql)
	if len(tokens) == 0 {
		return sql
	}
	quote := func(name string) string {
		name = strings.Trim(name, "'\"`")
		if d == DialectPostgres {
			return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		}
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	out := make([]string, 0, len(tokens))
	prevKW := ""
	for i, tok := range tokens {
		if reSimpleQuotedIdent.MatchString(tok) {
			inner := tok[1 : len(tok)-1]
			next := ""
			if i+1 < len(tokens) {
				next = tokens[i+1]
			}
			if shouldTreatQuotedAsIdent(prevKW, next) {
				out = append(out, quote(inner))
				if prevKW == "SELECT" || prevKW == "," {
					prevKW = "SELECT"
				} else if prevKW != "." {
					prevKW = ""
				} else {
					prevKW = ""
				}
				continue
			}
		}
		out = append(out, tok)
		up := strings.ToUpper(tok)
		switch {
		case isKeyword(tok):
			prevKW = up
		case tok == ".":
			prevKW = "."
		case tok == "," && (prevKW == "SELECT" || prevKW == ","):
			prevKW = "SELECT"
		case tok == "(" || tok == ")":
			if tok == ")" {
				prevKW = ""
			}
		case !isPunct(tok):
			if prevKW != "SELECT" {
				prevKW = ""
			}
		}
	}
	return joinSQLTokens(out)
}

func shouldTreatQuotedAsIdent(prevKW, next string) bool {
	switch prevKW {
	case "FROM", "JOIN", "UPDATE", "INTO", "TABLE", "AS", "ON", "BY", "SET",
		"WHERE", "AND", "OR", "HAVING", "SELECT", ",", ".", "PARTITION":
		return true
	}
	switch strings.ToUpper(next) {
	case "=", "!=", "<>", ">", "<", ">=", "<=", "LIKE", "IN", "IS", "BETWEEN", "NOT",
		",", "FROM", "AS", "JOIN", ".", "WHERE", "GROUP", "ORDER", "LIMIT", "HAVING", "UNION":
		return true
	}
	return false
}

func joinSQLTokens(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	var b strings.Builder
	for i, tok := range tokens {
		if i > 0 {
			prev := tokens[i-1]
			needSpace := true
			if prev == "(" || prev == "." || tok == ")" || tok == "," || tok == ";" || tok == "." {
				needSpace = false
			}
			if needSpace {
				b.WriteByte(' ')
			}
		}
		b.WriteString(tok)
	}
	return strings.TrimSpace(b.String())
}

func sqlHasUnboundPlaceholder(sql string) bool {
	_, found := replacePlaceholders(sql, func(string, bool) string { return "\x00" })
	return found
}

func substituteExplainPlaceholders(sql string) string {
	out, _ := replacePlaceholders(sql, func(prevSignificant string, _ bool) string {
		up := strings.ToUpper(strings.TrimSpace(prevSignificant))
		switch up {
		case "LIMIT", "OFFSET", "FETCH":
			return "100"
		case "LIKE", "ILIKE":
			return "'%'"
		case "DATE_FORMAT", "STR_TO_DATE", "TIME_FORMAT", "GET_FORMAT":
			// Second arg is typically a format string; first-arg placeholders still become NULL,
			// then refineDateProbeLiterals fixes DATE_FORMAT(x, NULL).
			return "'%Y-%m-%d'"
		}
		return "NULL"
	})
	return out
}

// replacePlaceholders replaces ? and $n outside comments/literals.
func replacePlaceholders(sql string, replacer func(prevSignificant string, isDollar bool) string) (string, bool) {
	runes := []rune(sql)
	n := len(runes)
	var b strings.Builder
	prevSig := ""
	found := false
	i := 0
	for i < n {
		r := runes[i]
		switch {
		case r == '-' && i+1 < n && runes[i+1] == '-':
			for i < n && runes[i] != '\n' {
				b.WriteRune(runes[i])
				i++
			}
		case r == '#':
			for i < n && runes[i] != '\n' {
				b.WriteRune(runes[i])
				i++
			}
		case r == '/' && i+1 < n && runes[i+1] == '*':
			b.WriteRune(runes[i])
			b.WriteRune(runes[i+1])
			i += 2
			for i+1 < n && !(runes[i] == '*' && runes[i+1] == '/') {
				b.WriteRune(runes[i])
				i++
			}
			if i+1 < n {
				b.WriteRune(runes[i])
				b.WriteRune(runes[i+1])
				i += 2
			}
		case r == '\'' || r == '"' || r == '`':
			q := r
			b.WriteRune(r)
			i++
			for i < n {
				b.WriteRune(runes[i])
				if runes[i] == '\\' && i+1 < n {
					i++
					b.WriteRune(runes[i])
					i++
					continue
				}
				if runes[i] == q {
					i++
					if q == '\'' && i < n && runes[i] == '\'' {
						b.WriteRune(runes[i])
						i++
						continue
					}
					break
				}
				i++
			}
		case r == '?':
			b.WriteString(replacer(prevSig, false))
			prevSig = ""
			found = true
			i++
		case r == '$' && i+1 < n && unicode.IsDigit(runes[i+1]):
			j := i + 1
			for j < n && unicode.IsDigit(runes[j]) {
				j++
			}
			b.WriteString(replacer(prevSig, true))
			prevSig = ""
			found = true
			i = j
		case unicode.IsLetter(r) || r == '_':
			j := i + 1
			for j < n && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
				j++
			}
			tok := string(runes[i:j])
			b.WriteString(tok)
			prevSig = tok
			i = j
		default:
			b.WriteRune(r)
			// Keep prevSig across whitespace and comparison ops so "LIMIT ?" / "col >= ?" work.
			// Also keep across '(' so DATE_FORMAT(col, ?) sees DATE_FORMAT as prev when hitting first
			// placeholder after '(' — actually first ? after ( has prev cleared below.
			// For "DATE_FORMAT(col, ?)" the prevSig before ? should be the identifier before comma;
			// we specially handle DATE_FORMAT via refineDateProbeLiterals instead.
			if !unicode.IsSpace(r) && r != ',' && r != '=' && r != '<' && r != '>' && r != '!' && r != '.' {
				if r == '(' || r == ')' || r == ';' {
					// Keep function name as prevSig across '(' so DATE_FORMAT( ? ) gets format probe
					// when placeholder is the first arg. For second arg after comma, prevSig becomes
					// the column name — refineDateProbeLiterals covers DATE_FORMAT(x, NULL).
					if r == ')' || r == ';' {
						prevSig = ""
					}
					// '(' : keep prevSig (function name)
				}
			}
			i++
		}
	}
	return b.String(), found
}
