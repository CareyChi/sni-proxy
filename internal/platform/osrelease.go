package platform

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

var allowedOSReleaseKeys = map[string]bool{
	"ID":          true,
	"ID_LIKE":     true,
	"NAME":        true,
	"PRETTY_NAME": true,
	"VERSION":     true,
	"VERSION_ID":  true,
}

func ParseOSRelease(reader io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(reader, 256<<10))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !allowedOSReleaseKeys[key] {
			continue
		}
		value, err := parseOSReleaseValue(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("parse os-release key %s: %w", key, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseOSReleaseValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw[0] == '\'' {
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return "", fmt.Errorf("unterminated single quote")
		}
		return raw[1 : len(raw)-1], nil
	}
	if raw[0] != '"' {
		if strings.ContainsAny(raw, " \t\r\n") {
			return "", fmt.Errorf("unquoted value contains whitespace")
		}
		return raw, nil
	}
	if len(raw) < 2 || raw[len(raw)-1] != '"' {
		return "", fmt.Errorf("unterminated double quote")
	}
	content := raw[1 : len(raw)-1]
	var builder strings.Builder
	for index := 0; index < len(content); index++ {
		if content[index] != '\\' {
			builder.WriteByte(content[index])
			continue
		}
		if index+1 >= len(content) {
			return "", fmt.Errorf("trailing escape")
		}
		next := content[index+1]
		if next == '\\' || next == '"' || next == '$' || next == '`' {
			builder.WriteByte(next)
			index++
			continue
		}
		builder.WriteByte('\\')
	}
	return builder.String(), nil
}
