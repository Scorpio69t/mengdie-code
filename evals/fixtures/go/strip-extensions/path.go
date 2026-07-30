package pathutil

import (
	"path/filepath"
	"strings"
)

func BaseName(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, filepath.Ext(name))
}