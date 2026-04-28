package filegate

import (
	"fmt"
	"net/url"
	"strings"
)

func encodeVirtualPath(v string) (string, error) {
	trimmed := strings.TrimSpace(v)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}
	parts := strings.Split(trimmed, "/")
	encoded := make([]string, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		encoded = append(encoded, p)
	}
	if len(encoded) == 0 {
		return "", fmt.Errorf("path is required")
	}
	return strings.Join(encoded, "/"), nil
}

func boolQuery(query url.Values, key string, value bool) {
	if value {
		query.Set(key, "true")
	}
}
