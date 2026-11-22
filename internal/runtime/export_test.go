package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirSaverNeverOverwrites(t *testing.T) {
	d := DirSaver{Dir: filepath.Join(t.TempDir(), "exports")}
	for i, want := range []string{"a-pack.zip", "a-pack-2.zip", "a-pack-3.zip"} {
		got, err := d.Save(context.Background(), "a-pack.zip", []byte{byte(i)})
		if err != nil || got != want {
			t.Fatalf("save %d: %q %v, want %q", i, got, err, want)
		}
	}
	first, _ := os.ReadFile(filepath.Join(d.Dir, "a-pack.zip"))
	if len(first) != 1 || first[0] != 0 {
		t.Fatal("the first archive was overwritten")
	}
}

func TestWriteChosen(t *testing.T) {
	if _, err := WriteChosen("", []byte("PK")); !errors.Is(err, ErrCancelled) {
		t.Fatalf("an empty choice is a cancel, got %v", err)
	}
	dir := t.TempDir()
	name, err := WriteChosen(filepath.Join(dir, "site"), []byte("PK"))
	if err != nil || name != "site.zip" {
		t.Fatalf("%q %v", name, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "site.zip")); err != nil {
		t.Fatal(err)
	}
}
