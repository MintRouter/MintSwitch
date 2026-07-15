package zed

// stripJSONC converts Zed's JSONC settings dialect into strict JSON by
// removing // and /* */ comments and trailing commas, while leaving string
// literals (including escapes) untouched. It does not attempt full JSON
// validation; malformed input is surfaced by the subsequent json.Unmarshal.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			// Scan to the "*/" terminator. An unterminated /* swallows the
			// rest of the input (the truncated JSON is then surfaced by the
			// caller's json.Unmarshal); only a found terminator advances past
			// its '/' — never past the end of the data.
			i += 2
			for i < len(data) && !(data[i] == '*' && i+1 < len(data) && data[i+1] == '/') {
				i++
			}
			if i < len(data) {
				i++ // skip the '/' of the "*/" terminator
			}
		default:
			out = append(out, c)
		}
	}
	return stripTrailingCommas(out)
}

// stripTrailingCommas removes commas that directly precede a closing brace or
// bracket (ignoring whitespace), outside of string literals.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\r' || data[j] == '\n') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
