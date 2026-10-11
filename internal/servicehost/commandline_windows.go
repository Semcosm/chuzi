//go:build windows

package servicehost

import "bytes"

// parseWindowsCommandLine parses the small, fixed SCM command line accepted
// by this package. It deliberately rejects unterminated quotes and control
// characters instead of attempting to interpret arbitrary shell syntax.
func parseWindowsCommandLine(input string) ([]string, bool) {
	args := make([]string, 0, 4)
	for index := 0; index < len(input); {
		for index < len(input) && (input[index] == ' ' || input[index] == '\t') {
			index++
		}
		if index == len(input) {
			break
		}
		var value []byte
		inQuotes := false
		for index < len(input) {
			if input[index] == '\r' || input[index] == '\n' {
				return nil, false
			}
			if input[index] == ' ' || input[index] == '\t' {
				if !inQuotes {
					break
				}
				value = append(value, input[index])
				index++
				continue
			}
			if input[index] == '\\' {
				start := index
				for index < len(input) && input[index] == '\\' {
					index++
				}
				count := index - start
				if index < len(input) && input[index] == '"' {
					value = append(value, bytes.Repeat([]byte{'\\'}, count/2)...)
					if count%2 == 1 {
						value = append(value, '"')
						index++
					} else {
						inQuotes = !inQuotes
						index++
					}
				} else {
					value = append(value, bytes.Repeat([]byte{'\\'}, count)...)
				}
				continue
			}
			if input[index] == '"' {
				inQuotes = !inQuotes
				index++
				continue
			}
			value = append(value, input[index])
			index++
		}
		if inQuotes {
			return nil, false
		}
		args = append(args, string(value))
		for index < len(input) && (input[index] == ' ' || input[index] == '\t') {
			index++
		}
	}
	return args, len(args) > 0
}
