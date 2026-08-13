// Command claudecode-remoted is the terminal session daemon
// (docs/ARCHITECTURE.md Section 5).
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Elias02345/remote-agent/daemon/internal/db"
	"github.com/Elias02345/remote-agent/daemon/internal/files"
	"github.com/Elias02345/remote-agent/daemon/internal/locks"
	"github.com/Elias02345/remote-agent/daemon/internal/terminal"
)

// splitRoots parses the --file-roots list, dropping empty entries so a
// trailing comma cannot turn into a root of "" (which would resolve to the
// working directory and quietly widen the allowlist).
func splitRoots(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

//go:embed web
var webFS embed.FS

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:8080", "listen address")
		dbPath  = flag.String("db", "/var/lib/claudecode-remote/daemon.db", "path to the SQLite database")
		lockDir = flag.String("lock-dir", locks.DefaultDir, "terminal lock directory")
		roots   = flag.String("file-roots", "/srv/exchange",
			"comma-separated allowlist of directories the file API may serve")
		uploadDir = flag.String("upload-dir", "/var/lib/claudecode-remote/uploads",
			"where partial resumable uploads are kept")
	)
	flag.Parse()

	// Never bind to a wildcard address. There is no authentication before
	// Phase 7, so "only reachable from localhost or the Tailscale interface"
	// is the entire access control story right now (CLAUDE.md guardrails).
	// Public reachability is CloudGate's job, and it forwards to a local port.
	if err := checkBindAddress(*addr); err != nil {
		log.Fatalf("refusing to start: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		log.Fatalf("create database directory: %v", err)
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	lockMgr, err := locks.New(*lockDir)
	if err != nil {
		log.Fatalf("lock directory: %v", err)
	}

	mgr := terminal.NewManager(database, lockMgr, terminal.NewTmux())

	mux := mgr.Routes()

	// The file API serves only the allowlisted roots — there is deliberately
	// no browser over the whole filesystem (Section 8.1).
	store, err := files.NewStore(splitRoots(*roots))
	if err != nil {
		log.Fatalf("file roots: %v", err)
	}
	uploader, err := files.NewUploader(store, *uploadDir)
	if err != nil {
		log.Fatalf("upload directory: %v", err)
	}
	(&files.API{Store: store, Uploader: uploader}).Register(mux)

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// The xterm.js test client from Phase 4. It exists to validate the
	// protocol before the Flutter app is built, not as a shipping UI.
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embedded web assets: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout on purpose: a terminal WebSocket is long-lived and
		// a write deadline would kill idle sessions.
	}

	log.Printf("claudecode-remoted listening on %s (db=%s, locks=%s)", *addr, *dbPath, lockMgr.Dir())
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// checkBindAddress rejects wildcard binds. Returns nil for loopback and for a
// concrete non-wildcard address such as a Tailscale IP.
func checkBindAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" || host == "*" {
		return errWildcardBind
	}
	return nil
}

type bindError string

func (e bindError) Error() string { return string(e) }

const errWildcardBind = bindError(
	"bind address must be localhost or a specific interface (e.g. the Tailscale IP), never a wildcard: " +
		"the daemon has no authentication before Phase 7, and public access is CloudGate's job")
