package git

import "os"

// writeFileHelper is in a separate file so tests in this package
// can share the helper without dragging it into production code.
func writeFileHelper(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}