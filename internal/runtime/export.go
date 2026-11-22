package runtime

// The native save of the ZIP export (capabilities.saveFile, contract cases/export.json): POST /v1/export?name=<file>
// with the archive. The Saver decides where it goes: the desktop app asks with a save dialog, the headless build writes
// to its configured folder (DirSaver). The answer names the saved file only, never a path.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const exportPath = "/v1/export"

// ErrCancelled means the person closed the save dialog: nothing was written.
var ErrCancelled = errors.New("save cancelled")

// Saver keeps an archive under a suggested plain file name and answers the name it was saved as.
type Saver interface {
	Save(ctx context.Context, name string, data []byte) (string, error)
}

// SaverFunc adapts a function to a Saver.
type SaverFunc func(ctx context.Context, name string, data []byte) (string, error)

// Save calls f.
func (f SaverFunc) Save(ctx context.Context, name string, data []byte) (string, error) {
	return f(ctx, name, data)
}

// DirSaver writes into one folder and never overwrites: a taken name gets -2, -3, … before .zip.
type DirSaver struct {
	Dir string
}

// Save writes data as the first free name in the folder.
func (d DirSaver) Save(_ context.Context, name string, data []byte) (string, error) {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return "", err
	}
	stem := strings.TrimSuffix(name, ".zip")
	for i := 1; i < 1000; i++ {
		candidate := name
		if i > 1 {
			candidate = stem + "-" + strconv.Itoa(i) + ".zip"
		}
		f, err := os.OpenFile(filepath.Join(d.Dir, candidate), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(f.Name())
			return "", errors.Join(werr, cerr)
		}
		return candidate, nil
	}
	return "", errors.New("no free file name")
}

// WriteChosen writes data to a path the person picked in a save dialog (overwriting is the dialog's question).
func WriteChosen(path string, data []byte) (string, error) {
	if path == "" {
		return "", ErrCancelled
	}
	if !strings.EqualFold(filepath.Ext(path), ".zip") {
		path += ".zip"
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return filepath.Base(path), nil
}

func isZip(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte("PK\x05\x06"))
}

func exportError(w http.ResponseWriter, status int, code, message string) {
	mediaJSON(w, status, obj{"ok": false, "code": code, "message": message})
}

func (s *Server) export(w http.ResponseWriter, r *http.Request, method string) {
	if s.saver == nil {
		forwardError(w, http.StatusNotFound, "not_found", "Unknown runtime path.")
		return
	}
	if method != http.MethodPost {
		w.Header().Set("allow", "POST")
		forwardError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The export answers POST only.")
		return
	}
	e := s.constants.Export
	name := r.URL.Query().Get("name")
	if !r.URL.Query().Has("name") || name == "" {
		name = e.FallbackName
	}
	if !s.exportName.MatchString(name) {
		exportError(w, http.StatusUnprocessableEntity, "export_bad_name", "The file name must be one plain name ending in .zip.")
		return
	}
	tooLarge := "The archive is empty or larger than " + strconv.FormatInt(e.MaxBytes/1_000_000, 10) + " MB."
	if r.ContentLength > e.MaxBytes {
		exportError(w, http.StatusRequestEntityTooLarge, "export_too_large", tooLarge)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, e.MaxBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > e.MaxBytes {
		exportError(w, http.StatusRequestEntityTooLarge, "export_too_large", tooLarge)
		return
	}
	if !isZip(data) {
		exportError(w, http.StatusUnsupportedMediaType, "export_not_zip", "The body must be a ZIP archive.")
		return
	}
	saved, err := s.saver.Save(r.Context(), name, data)
	if errors.Is(err, ErrCancelled) {
		mediaJSON(w, http.StatusOK, obj{"ok": true, "saved": false, "code": "cancelled", "message": "Nothing was saved."})
		return
	}
	if err != nil {
		exportError(w, http.StatusInternalServerError, "export_failed", "The archive could not be saved.")
		return
	}
	mediaJSON(w, http.StatusOK, obj{"ok": true, "saved": true, "name": saved, "bytes": len(data)})
}

func compileExportName(pattern string) *regexp.Regexp {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return regexp.MustCompile(`^$`)
	}
	return re
}
