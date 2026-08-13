package terminal

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
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
// There is NO authentication here yet — that is Phase 7. Until then the fact
// that the daemon binds to localhost only is the entire access control story,
// which is why the bind address is not configurable to anything wider.
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
	// Same-origin checking is meaningless while the daemon is reachable only
	// on localhost and has no auth at all (Phase 7 adds device
	// challenge-response). Revisit this together with authentication rather
	// than pretending an origin check is protecting anything today.
	CheckOrigin: func(*http.Request) bool { return true },
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
	// until the next redraw.
	if snapshot, err := m.Tmux.CapturePane(s.TmuxSession); err == nil && len(snapshot) > 0 {
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
