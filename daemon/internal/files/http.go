package files

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// chunkReadTimeout bounds how long one PATCH body may take to arrive. Generous
// on purpose: a phone on a bad connection uploading a large chunk is normal,
// an hour of silence is not.
const chunkReadTimeout = 15 * time.Minute

// API serves the file endpoints from Section 8.1.
//
// Like the terminal API, this carries no authentication before Phase 7
// (gap G-02). It is reachable only from localhost until then, and Phase 7
// applies the same Ed25519 device challenge-response to these routes as to the
// terminal WebSocket — an unauthenticated file API would otherwise sidestep
// the entire device pairing model.
type API struct {
	Store    *Store
	Uploader *Uploader
}

// Register mounts the file routes on a mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("/files", a.handleFiles)
	mux.HandleFunc("/files/download", a.handleDownload)
	mux.HandleFunc("/files/upload", a.handleUploadCreate)
	mux.HandleFunc("/files/upload/", a.handleUploadResume)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusFor keeps a path-traversal attempt from being reported as a server
// error: it is a refused request, and the distinction matters in logs. The
// same applies to the two ways a client can ask for an upload we will not
// accept — both are bad requests, not failures on our side.
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrOutsideRoots):
		return http.StatusForbidden
	case errors.Is(err, ErrMissingChecksum):
		return http.StatusBadRequest
	case errors.Is(err, ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}

func (a *API) handleFiles(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "."
	}

	switch r.Method {
	case http.MethodGet:
		entries, err := a.Store.List(p)
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, entries)

	case http.MethodDelete:
		if err := a.Store.Delete(p); err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}

	rc, size, sum, err := a.Store.Open(p)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	defer rc.Close()

	// The hash travels in a header, before the body, so the client can verify
	// what it received (Section 8.2). A trailer would arrive after the client
	// has already written the file out.
	w.Header().Set("X-Checksum-SHA256", sum)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(p)))

	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("stream %s: %v", p, err)
	}
}

func setTusHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", TusVersion)
	w.Header().Set("Tus-Version", TusVersion)
}

// handleUploadCreate implements the tus creation extension.
//
// Headers: Upload-Length (required, bounded by MaxUploadBytes) and
// Upload-Metadata carrying a "filename" key and a "sha256" key holding the
// hash the client computed before sending. Both metadata keys are required —
// see Uploader.Create for why the hash is not optional.
func (a *API) handleUploadCreate(w http.ResponseWriter, r *http.Request) {
	setTusHeaders(w)

	if r.Method == http.MethodOptions {
		w.Header().Set("Tus-Extension", "creation")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	length, err := ParseOffset(r.Header.Get("Upload-Length"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Upload-Length is required and must be a non-negative integer")
		return
	}

	meta := ParseUploadMetadata(r.Header.Get("Upload-Metadata"))
	target := meta["filename"]
	if target == "" {
		writeErr(w, http.StatusBadRequest, "Upload-Metadata must contain a filename")
		return
	}
	if dir := meta["dir"]; dir != "" {
		target = path.Join(dir, target)
	}

	id, err := newUploadID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := a.Uploader.Create(id, target, length, meta["sha256"]); err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}

	w.Header().Set("Location", "/files/upload/"+id)
	w.Header().Set("Upload-Offset", "0")
	w.WriteHeader(http.StatusCreated)
}

func (a *API) handleUploadResume(w http.ResponseWriter, r *http.Request) {
	setTusHeaders(w)

	id := strings.TrimPrefix(r.URL.Path, "/files/upload/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodHead:
		offset, length, err := a.Uploader.Offset(id)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Without no-store a cached HEAD would hand a resuming client a stale
		// offset, and it would resume from the wrong place.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Upload-Offset", strconv.FormatInt(offset, 10))
		w.Header().Set("Upload-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusOK)

	case http.MethodPatch:
		if ct := r.Header.Get("Content-Type"); ct != "" && ct != "application/offset+octet-stream" {
			writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/offset+octet-stream")
			return
		}
		offset, err := ParseOffset(r.Header.Get("Upload-Offset"))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "Upload-Offset is required")
			return
		}

		// The server has no ReadTimeout — it cannot have one, because the
		// terminal WebSocket is long-lived and a global read deadline would
		// kill idle sessions. That leaves this handler as the only place a
		// slow-body client can be bounded, and without a bound one open PATCH
		// that sends a byte a minute holds a connection and a file handle
		// indefinitely, for free.
		//
		// Per-request, so it constrains the upload body and nothing else.
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(chunkReadTimeout)); err != nil {
			log.Printf("set read deadline for upload %s: %v", id, err)
		}

		newOffset, done, err := a.Uploader.WriteChunk(id, offset, r.Body)
		if errors.Is(err, ErrOffsetConflict) {
			w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
			writeErr(w, http.StatusConflict, "offset conflict; resume from Upload-Offset")
			return
		}
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}

		w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
		if !done {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		sum, err := a.Uploader.Finish(id)
		// The server's own hash goes back either way, so the client can do the
		// second comparison from Section 8.2 step 4 instead of only being told
		// whether it passed.
		w.Header().Set("X-Checksum-SHA256", sum)
		if errors.Is(err, ErrChecksumMismatch) {
			writeErr(w, http.StatusUnprocessableEntity,
				"checksum mismatch; the upload was discarded, send it again")
			return
		}
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		a.Uploader.Discard(id)
		w.WriteHeader(http.StatusNoContent)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func newUploadID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate upload id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
