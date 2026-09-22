package main

import (
	"os"
	"regexp"
	"strings"
)

func mkdirAll(path string) error { return os.MkdirAll(path, 0o700) }

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonSlug.ReplaceAllString(value, "-")
	return strings.Trim(value, "-")
}
