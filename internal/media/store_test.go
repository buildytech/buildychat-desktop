package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreIsContentAddressed(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "media")}
	a, err := s.Put([]byte("png bytes"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Put([]byte("png bytes"), "image/png")
	if err != nil || a != b || !ValidName(a) || filepath.Ext(a) != ".png" {
		t.Fatalf("names %q %q (%v)", a, b, err)
	}
	if _, err := s.Put([]byte("x"), "text/html"); err == nil {
		t.Fatal("a non-picture type was stored")
	}
	data, typ, err := s.Get(a)
	if err != nil || string(data) != "png bytes" || typ != "image/png" {
		t.Fatalf("get: %q %q %v", data, typ, err)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 {
		t.Fatalf("%d files in the store, want 1 (no temp files left)", len(entries))
	}
	for _, bad := range []string{"../" + a, a + "/", "A" + a[1:], a[:64] + ".exe", ""} {
		if _, _, err := s.Get(bad); err == nil {
			t.Errorf("%q was served", bad)
		}
	}
	if NameOfAddress(FilesPrefix+a) != a || NameOfAddress("/runtime/v1/files/../"+a) != "" || NameOfAddress(a) != "" {
		t.Error("NameOfAddress accepts a non-store address")
	}
}
