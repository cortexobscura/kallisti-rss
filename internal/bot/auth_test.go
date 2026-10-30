package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Secret-file errors name the configured path, and where a relative path
// resolved, so a host-relative path inside a container is obvious. They never
// include the file contents.
func TestSecretErrorsNamePathNotContents(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, e := readSecret("./secrets/password")
	if e == nil || !strings.Contains(e.Error(), `"./secrets/password"`) || !strings.Contains(e.Error(), filepath.Join(dir, "secrets", "password")) {
		t.Fatalf("missing configured or resolved path: %v", e)
	}
	abs := filepath.Join(dir, "absent")
	if _, e = readSecret(abs); e == nil || strings.Contains(e.Error(), "resolved") || !strings.Contains(e.Error(), abs) {
		t.Fatalf("absolute path error: %v", e)
	}
	p := filepath.Join(dir, "multi")
	must(t, os.WriteFile(p, []byte("first-secret\nsecond-secret\n"), 0600))
	if _, e = readSecret(p); e == nil || strings.Contains(e.Error(), "secret\n") || strings.Contains(e.Error(), "first-secret") || !strings.Contains(e.Error(), p) {
		t.Fatalf("multi-line error leaked or lost path: %v", e)
	}
}
