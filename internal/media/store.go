package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FilesPrefix is where the store is served (GET /runtime/v1/files/<name>).
const FilesPrefix = "/runtime/v1/files/"

var (
	nameRE = regexp.MustCompile(`^[a-f0-9]{64}\.(jpg|png|webp|gif|avif)$`)
	exts   = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/gif": "gif", "image/avif": "avif"}
	types  = map[string]string{"jpg": "image/jpeg", "png": "image/png", "webp": "image/webp", "gif": "image/gif", "avif": "image/avif"}
)

// ErrNotStored means the name is valid but no such file is in the store.
var ErrNotStored = errors.New("not stored")

// Store keeps pictures by the sha256 of their bytes: a name never changes its content, so it is served immutable.
type Store struct {
	Dir string
}

// DefaultDir is <UserConfigDir>/BuildY/media.
func DefaultDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "BuildY", "media")
}

// ValidName reports whether a name is one the store can hold (64 lower-case hex, a picture extension).
func ValidName(name string) bool { return nameRE.MatchString(name) }

// NameOfAddress is the file name of a store address (/runtime/v1/files/<name>), or "" when it is not one.
func NameOfAddress(address string) string {
	name, ok := strings.CutPrefix(address, FilesPrefix)
	if !ok || !ValidName(name) {
		return ""
	}
	return name
}

// TypeOf is the picture type of a stored name.
func TypeOf(name string) string { return types[name[strings.LastIndexByte(name, '.')+1:]] }

// Put stores bytes of a picture type and answers their name; storing the same bytes again is a no-op.
func (s Store) Put(data []byte, typ string) (string, error) {
	ext, ok := exts[typ]
	if !ok {
		return "", errors.New("not a picture type")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + "." + ext
	path := filepath.Join(s.Dir, name)
	if _, err := os.Stat(path); err == nil {
		return name, nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(s.Dir, ".put-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return "", errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return name, nil
}

// Get reads a stored picture and its type.
func (s Store) Get(name string) ([]byte, string, error) {
	if !ValidName(name) {
		return nil, "", ErrNotStored
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, name))
	if err != nil {
		return nil, "", ErrNotStored
	}
	return data, TypeOf(name), nil
}
