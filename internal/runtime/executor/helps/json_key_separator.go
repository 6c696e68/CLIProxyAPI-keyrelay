package helps

import "encoding/json"

// RepairObjectKeySeparators restores ":" after an object key when the payload
// has "," there instead. Upstream response.completed events have arrived in
// that shape: string and object values lose the colon, numeric values keep it.
// The bytes are returned only when the repair produces valid JSON.
func RepairObjectKeySeparators(src []byte) ([]byte, bool) {
	if len(src) == 0 || json.Valid(src) {
		return nil, false
	}
	out := append([]byte(nil), src...)
	changed := false
	var stack []byte
	state := keyRepairValue
	inString := false
	escaped := false
	stringIsKey := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
				if stringIsKey {
					state = keyRepairColon
				} else {
					state = keyRepairComma
				}
			}
			continue
		}
		switch c {
		case ' ', '\n', '\r', '\t':
			continue
		case '"':
			inString = true
			stringIsKey = state == keyRepairKey
		case '{':
			stack = append(stack, '{')
			state = keyRepairKey
		case '[':
			stack = append(stack, '[')
			state = keyRepairValue
		case ':':
			if state == keyRepairColon {
				state = keyRepairValue
			}
		case ',':
			if state == keyRepairColon && len(stack) > 0 && stack[len(stack)-1] == '{' {
				out[i] = ':'
				changed = true
				state = keyRepairValue
				continue
			}
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				state = keyRepairKey
			} else {
				state = keyRepairValue
			}
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			state = keyRepairComma
		default:
			if state == keyRepairValue || state == keyRepairColon {
				state = keyRepairComma
			}
		}
	}
	if !changed || !json.Valid(out) {
		return nil, false
	}
	return out, true
}

const (
	keyRepairValue = iota
	keyRepairKey
	keyRepairColon
	keyRepairComma
)
