package datasrc

import (
	"fmt"
	"regexp"
	"strings"

	"bywork/internal/expr"
)

// Compile turns a configured SQL with {{var}} placeholders into a
// parameterised query. Rules (design §安全与边界):
//   - only a single SELECT / WITH statement;
//   - {{var}} may only appear in value positions and is always bound as a
//     placeholder, never spliced into identifiers or statement structure.
func Compile(sql string, vars expr.Vars) (string, []any, error) {
	clean, err := checkReadOnly(sql)
	if err != nil {
		return "", nil, err
	}
	var out strings.Builder
	var args []any
	inQuote := byte(0)
	for i := 0; i < len(clean); i++ {
		c := clean[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			if c == '{' && i+1 < len(clean) && clean[i+1] == '{' {
				return "", nil, fmt.Errorf("变量不能放在字符串字面量里，请写成 = {{var}} 由系统绑定")
			}
			out.WriteByte(c)
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			inQuote = c
			out.WriteByte(c)
			continue
		}
		if c == '{' && i+1 < len(clean) && clean[i+1] == '{' {
			end := strings.Index(clean[i:], "}}")
			if end < 0 {
				return "", nil, fmt.Errorf("未闭合的 {{")
			}
			name := strings.TrimSpace(clean[i+2 : i+end])
			if !identRe.MatchString(name) {
				return "", nil, fmt.Errorf("非法变量名 %q", name)
			}
			if !valuePosition(out.String()) {
				return "", nil, fmt.Errorf("变量 {{%s}} 只能出现在值的位置（如 = / IN ( / > 之后），不能作为表名、列名或语句结构", name)
			}
			v, _ := vars.Get(name)
			args = append(args, v)
			out.WriteString("?")
			i += end + 1
			continue
		}
		out.WriteByte(c)
	}
	if inQuote != 0 {
		return "", nil, fmt.Errorf("未闭合的引号")
	}
	return out.String(), args, nil
}

// Vars returns the {{var}} names a query references.
func Vars(sql string) []string { return expr.TemplateFields(sql) }

var (
	identRe     = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)
	lineComment = regexp.MustCompile(`--[^\n]*`)
	blockCmt    = regexp.MustCompile(`(?s)/\*.*?\*/`)
	wordRe      = regexp.MustCompile(`[A-Za-z_]+`)
	forbidden   = map[string]bool{
		"INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "DROP": true, "ALTER": true,
		"CREATE": true, "TRUNCATE": true, "GRANT": true, "REVOKE": true, "INTO": true, "OUTFILE": true,
		"LOCK": true, "CALL": true, "SET": true, "HANDLER": true, "LOAD": true, "ATTACH": true, "PRAGMA": true,
	}
	valueKeywords = map[string]bool{
		"IN": true, "LIKE": true, "BETWEEN": true, "AND": true, "OR": true, "NOT": true, "WHEN": true,
		"THEN": true, "ELSE": true, "LIMIT": true, "OFFSET": true, "IS": true, "SELECT": true,
	}
)

func checkReadOnly(sql string) (string, error) {
	s := blockCmt.ReplaceAllString(sql, " ")
	s = lineComment.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ";")
	if strings.Contains(stripQuoted(s), ";") {
		return "", fmt.Errorf("只允许单条语句")
	}
	words := wordRe.FindAllString(stripQuoted(s), -1)
	if len(words) == 0 {
		return "", fmt.Errorf("查询为空")
	}
	first := strings.ToUpper(words[0])
	if first != "SELECT" && first != "WITH" {
		return "", fmt.Errorf("只允许 SELECT 查询")
	}
	for i, w := range words {
		u := strings.ToUpper(w)
		if forbidden[u] {
			return "", fmt.Errorf("只读数据源不允许 %s", u)
		}
		if u == "FOR" && i+1 < len(words) && strings.ToUpper(words[i+1]) == "UPDATE" {
			return "", fmt.Errorf("只读数据源不允许 FOR UPDATE")
		}
	}
	return s, nil
}

func stripQuoted(s string) string {
	var b strings.Builder
	q := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if q != 0 {
			if c == q {
				q = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			q = c
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// valuePosition reports whether the text before a placeholder ends in an
// operator or keyword after which a value (not an identifier) is expected.
func valuePosition(prefix string) bool {
	p := strings.TrimRight(prefix, " \t\r\n")
	if p == "" {
		return false
	}
	last := p[len(p)-1]
	if strings.ContainsRune("=<>(,+-*/", rune(last)) {
		return true
	}
	words := wordRe.FindAllString(p, -1)
	if len(words) == 0 || !strings.HasSuffix(p, words[len(words)-1]) {
		return false
	}
	return valueKeywords[strings.ToUpper(words[len(words)-1])]
}
