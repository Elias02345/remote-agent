package terminal

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Routes returns the REST and WebSocket surface from Section 5.2:
//
//	GET    /sessions              list open terminals
//	POST   /sessions              new terminal (name, cwd, shell)
//	DELETE /sessions/{id}         close a terminal (the only way to close one)
//	PATCH  /sessions/{id}         rename
//	GET    /sessions/{id}/stream  WebSocket, raw byte passthrough
//
// These handlers carry no authentication of their own. That is deliberate and
// not a gap: `cmd/claudecode-remoted` wraps this whole mux in
// `identity.RequireDevice`, so the check happens once, in one place, for the
// terminal API and the file API alike. Adding a second check here would give
// two places to keep in agreement.
//
// The consequence is that this mux must never be mounted directly.
func (m *Manager) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions", m.handleSessions)
	mux.HandleFunc("/sessions/", m.handleSession)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (m *Manager) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := m.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		var body struct {
			Name  string `json:"name"`
			Cwd   string `json:"cwd"`
			Shell string `json:"shell"`
		}
		// An empty body is a legitimate "just give me a shell".
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		s, err := m.Create(body.Name, body.Cwd, body.Shell)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, s)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// splitSessionPath turns "/sessions/<id>" or "/sessions/<id>/stream" into its
// parts. Returns ok=false for anything else.
func splitSessionPath(path string) (id string, tail string, ok bool) {
	rest := strings.TrimPrefix(path, "/sessions/")
	if rest == "" || rest == path {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	if parts[0] == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		return parts[0], parts[1], true
	}
	return parts[0], "", true
}

func (m *Manager) handleSession(w http.ResponseWriter, r *http.Request) {
	id, tail, ok := splitSessionPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if tail == "stream" {
		m.handleStream(w, r, id)
		return
	}
	if tail != "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if err := m.Close(id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "no such open session")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPatch:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := m.Rename(id, body.Name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "no such session")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s, err := m.Get(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s)

	case http.MethodGet:
		s, err := m.Get(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "no such session")
			return
		}
		writeJSON(w, http.StatusOK, s)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- WebSocket stream --------------------------------------------------------

// clientMessage is what the client sends (Section 5.2).
type clientMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// serverMessage is what the daemon sends back.
type serverMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     sameOriginOrNone,
}

// sameOriginOrNone is the upgrader's origin check.
//
// A WebSocket handshake is not subject to the same-origin policy, so without
// this any page the operator visits could open a socket to the daemon and have
// the browser attach its ambient credentials — cross-site WebSocket hijacking.
// The single-use ticket already blocks the attack (a hostile page cannot obtain
// one), but relying on a single control for something this cheap to check twice
// is how a later refactor quietly removes the only guard.
//
// The rule:
//
//   - No Origin header at all — allow. Native clients (the Flutter app, curl,
//     the Go tests) do not send one; only browsers do. An absent Origin is
//     therefore not a browser and not a cross-site request.
//   - Origin's host equals the request's Host — allow. This is self-configuring:
//     it works for 127.0.0.1:8080 in development and for the public domain
//     behind the tunnel, with nothing to keep in sync.
//   - Anything else — refuse.
func sameOriginOrNone(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// Compare hosts, not the full URL: the tunnel terminates TLS at
	// Cloudflare's edge and forwards plaintext, so the scheme the browser saw
	// (https) is not the scheme this daemon sees.
	return strings.EqualFold(u.Host, r.Host)
}

// HandleHealthWS accepts a WebSocket upgrade, sends one message and closes.
//
// It exists for server-provisioning/cloudgate/verify-tunnel.sh: proving that
// Upgrade headers survive CloudGate needs an upgrade that actually completes,
// and every session route requires a single-use ticket the operator cannot
// obtain before pairing their first device. Rather than weakening that, this is
// a route with nothing behind it — no session lookup, no tmux, no PTY, no
// reads from the client. The upgrader's same-origin check still applies.
func HandleHealthWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written a response by this point.
		return
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = conn.WriteJSON(serverMessage{Type: "health", Data: "ok"})
}

// handleStream pumps raw bytes between the client and a tmux attachment.
//
// Terminal output is base64-encoded inside the JSON envelope because PTY
// output is arbitrary binary, not valid UTF-8 — encoding/json would corrupt
// invalid sequences into U+FFFD, and a corrupted escape sequence is a garbled
// screen. The bytes themselves are never inspected or rewritten.
func (m *Manager) handleStream(w http.ResponseWriter, r *http.Request, id string) {
	s, err := m.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such session")
		return
	}
	if s.Status != "open" {
		writeError(w, http.StatusGone, "session is closed")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade for %s: %v", id, err)
		return
	}
	defer conn.Close()

	// The snapshot is taken BEFORE attaching, not after.
	//
	// Order matters here and the obvious order is wrong. Attaching starts tmux
	// writing into the PTY immediately — including its own initial full redraw
	// — so a snapshot captured afterwards describes a screen that is newer than
	// bytes already sitting in the PTY buffer, waiting to be read. The client
	// would then receive the newer state first and the older redraw second, and
	// paint the stale one over it. Capturing first means everything the client
	// sees after the snapshot is strictly newer than the snapshot.
	snapshot, snapErr := m.Tmux.CapturePane(s.TmuxSession)
	if snapErr != nil {
		// Not fatal: a session that cannot be captured can still be attached,
		// and the client simply waits for the next redraw instead of getting
		// the current screen immediately.
		log.Printf("capture-pane %s: %v", id, snapErr)
	}

	att, err := m.Tmux.Attach(s.TmuxSession, 80, 24)
	if err != nil {
		log.Printf("attach %s: %v", id, err)
		return
	}
	// Closing the attachment detaches this client only. The tmux session keeps
	// running, which is what makes a dropped connection harmless.
	defer att.Close()

	var writeMu sync.Mutex
	send := func(msg serverMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(msg)
	}

	// Deliver the current screen before the live stream, so a reconnecting
	// client sees the present state immediately instead of an empty terminal
	// until the next redraw. Sent before the reader goroutine starts, so it
	// cannot interleave with live output.
	if len(snapshot) > 0 {
		if err := send(serverMessage{Type: "output", Data: base64.StdEncoding.EncodeToString(snapshot)}); err != nil {
			return
		}
	}

	done := make(chan struct{})

	// PTY -> client
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := att.PTY.Read(buf)
			if n > 0 {
				if err := send(serverMessage{
					Type: "output",
					Data: base64.StdEncoding.EncodeToString(buf[:n]),
				}); err != nil {
					return
				}
			}
			if err != nil {
				_ = send(serverMessage{Type: "exit"})
				return
			}
		}
	}()

	// client -> PTY
	for {
		var msg clientMessage
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		switch msg.Type {
		case "input":
			raw, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				// Older clients and hand-written test clients send plain text;
				// accept it rather than dropping keystrokes silently.
				raw = []byte(msg.Data)
			}
			if _, err := att.PTY.Write(raw); err != nil {
				break
			}
			_ = m.DB.TouchSession(id)
		case "resize":
			if err := att.Resize(msg.Cols, msg.Rows); err != nil {
				log.Printf("resize %s: %v", id, err)
			}
			_ = m.DB.TouchSession(id)
		}
	}

	// Give the reader goroutine a moment to notice the closed PTY.
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}
