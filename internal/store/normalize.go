package store

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	inList = regexp.MustCompile(`(?i)\bIN\s*\(\s*\?(\s*,\s*\?)*\s*\)`)
	values = regexp.MustCompile(`(?i)\bVALUES\s*(\(\s*\?(\s*,\s*\?)*\s*\)\s*,?\s*)+`)
	spaces = regexp.MustCompile(`\s+`)
)

func Normalize(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			j := i + 1
			for j < len(q) {
				if q[j] == '\'' {
					if j+1 < len(q) && q[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			b.WriteByte('?')
			i = j
		case c == '"' || c == '`':
			j := strings.IndexByte(q[i+1:], c)
			if j < 0 {
				b.WriteString(q[i:])
				i = len(q)
				continue
			}
			b.WriteString(q[i : i+j+2])
			i += j + 1
		case c == '$' && i+1 < len(q) && isDigit(q[i+1]):
			b.WriteByte('$')
			for i+1 < len(q) && isDigit(q[i+1]) {
				i++
				b.WriteByte(q[i])
			}
		case isDigit(c) && (i == 0 || !isIdent(q[i-1])):
			for i+1 < len(q) && (isDigit(q[i+1]) || q[i+1] == '.') {
				i++
			}
			b.WriteByte('?')
		default:
			b.WriteByte(c)
		}
	}
	out := spaces.ReplaceAllString(strings.TrimSpace(b.String()), " ")
	out = inList.ReplaceAllString(out, "IN (?)")
	out = values.ReplaceAllString(out, "VALUES (?) ")
	return strings.TrimSpace(out)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdent(c byte) bool {
	return c == '_' || c == '.' || isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

func sortStrings(s []string) { sort.Strings(s) }
