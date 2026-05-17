package graphql

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// EncodeID builds a Relay global ID: base64("<typeName>:<numericId>").
func EncodeID(typeName string, id int64) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", typeName, id)))
}

// DecodeID parses a Relay global ID back into (typeName, numericId).
func DecodeID(gid string) (string, int64, error) {
	raw, err := base64.StdEncoding.DecodeString(gid)
	if err != nil {
		return "", 0, fmt.Errorf("decode id: %w", err)
	}
	s := string(raw)
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, errors.New("invalid relay id: no ':' separator")
	}
	n, err := strconv.ParseInt(s[i+1:], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("parse numeric id: %w", err)
	}
	return s[:i], n, nil
}
