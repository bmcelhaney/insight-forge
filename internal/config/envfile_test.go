package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFileSetsUnsetKeysOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.partsbase")
	content := `
# comment
export IF_PARTSBASE_CLIENT_ID=from-file
IF_PARTSBASE_USERNAME="user-from-file"
IF_PARTSBASE_PASSWORD=secret#not-a-comment-if-unquoted-hash-mid
IF_PARTSBASE_CLIENT_SECRET=abc
`
	// Fix password line - unquoted hash starts comment in our parser for trailing comments
	content = `
# comment
export IF_PARTSBASE_CLIENT_ID=from-file
IF_PARTSBASE_USERNAME="user-from-file"
IF_PARTSBASE_PASSWORD='p@ss#word'
IF_PARTSBASE_CLIENT_SECRET=abc
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pre-set one key — file must not override it.
	t.Setenv("IF_PARTSBASE_CLIENT_ID", "from-process")
	os.Unsetenv("IF_PARTSBASE_USERNAME")
	os.Unsetenv("IF_PARTSBASE_PASSWORD")
	os.Unsetenv("IF_PARTSBASE_CLIENT_SECRET")

	n, err := loadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("expected at least 3 keys set from file, got %d", n)
	}
	if got := os.Getenv("IF_PARTSBASE_CLIENT_ID"); got != "from-process" {
		t.Fatalf("process env should win, got %q", got)
	}
	if got := os.Getenv("IF_PARTSBASE_USERNAME"); got != "user-from-file" {
		t.Fatalf("username %q", got)
	}
	if got := os.Getenv("IF_PARTSBASE_PASSWORD"); got != "p@ss#word" {
		t.Fatalf("password %q", got)
	}
	if got := os.Getenv("IF_PARTSBASE_CLIENT_SECRET"); got != "abc" {
		t.Fatalf("secret %q", got)
	}
}

func TestUnquoteEnvValue(t *testing.T) {
	if got := unquoteEnvValue(`"hello"`); got != "hello" {
		t.Fatalf("got %q", got)
	}
	if got := unquoteEnvValue(`'x'`); got != "x" {
		t.Fatalf("got %q", got)
	}
	if got := unquoteEnvValue(`plain`); got != "plain" {
		t.Fatalf("got %q", got)
	}
}
