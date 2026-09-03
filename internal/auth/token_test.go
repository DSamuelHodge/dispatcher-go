package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/auth"
)

func TestLoadOrCreateAndValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".agent-token")
	tok, err := auth.LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !tok.Valid(tok.String()) {
		t.Fatal("self valid")
	}
	if tok.Valid("nope") {
		t.Fatal("bad token accepted")
	}
	// reload
	tok2, err := auth.LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if tok.String() != tok2.String() {
		t.Fatal("token changed on reload")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("token perms too open: %v", st.Mode())
	}
}
