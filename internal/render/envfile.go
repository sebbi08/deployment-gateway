package render

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// EnvFilePath is the host path of a service's secrets env file. It is built
// only from the gateway-controlled secrets dir and the validated service name.
func EnvFilePath(secretsDir, svc string) string {
	return filepath.Join(secretsDir, svc+".env")
}

// EnvFile renders secret values as a raw (format: raw) env file: "K=v\n"
// lines sorted by key, with no quoting or escaping.
func EnvFile(values map[string]string) []byte {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(values)) {
		b.WriteString(k + "=" + values[k] + "\n")
	}
	return []byte(b.String())
}
