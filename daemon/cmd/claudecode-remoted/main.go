// Command claudecode-remoted is the terminal session daemon
// (docs/ARCHITECTURE.md Section 5).
package main

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Elias02345/remote-agent/daemon/internal/db"
	"github.com/Elias02345/remote-agent/daemon/internal/files"
	"github.com/Elias02345/remote-agent/daemon/internal/identity"
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

		ownerEmail = flag.String("owner-email", "",
			"owner account email for device pairing (Section 10.2); pairing stays disabled until this and the password hash are set. "+
				"Prefer the CCR_OWNER_EMAIL environment variable.")
		ownerPasswordHash = flag.String("owner-password-hash", "",
			"Argon2id hash of the owner password (see identity.HashPassword); the password pairing factor is unsatisfiable while this is empty. "+
				"SECRET — prefer CCR_OWNER_PASSWORD_HASH; a flag value is world-readable in /proc/<pid>/cmdline.")
		ownerTOTPSecret = flag.String("owner-totp-secret", "",
			"RFC 6238 TOTP secret for the owner account (see identity.GenerateTOTPSecret); the TOTP pairing factor is unsatisfiable while this is empty. "+
				"SECRET — prefer CCR_OWNER_TOTP_SECRET; a flag value is world-readable in /proc/<pid>/cmdline.")

		setupOwner = flag.Bool("setup-owner", false,
			"generate the owner credentials (Argon2id hash + TOTP secret) and exit; reads the password from stdin")

		insecureNoAuth = flag.Bool("insecure-no-auth", false,
			"DANGER: disable device authentication on /sessions and /files, for the xterm.js test client only. "+
				"Refuses to start on a non-loopback bind address. Never the default.")

		trustedProxies = flag.String("trusted-proxies", "",
			"comma-separated CIDRs (or bare IPs) permitted to set CF-Connecting-IP / X-Forwarded-For. "+
				"Set this to the address CloudGate connects from, otherwise every request through the tunnel "+
				"shares one rate-limit bucket. Empty means no header is trusted.")

		publicDomain = flag.String("public-domain", "",
			"public domain this installation is reachable at (CCR_PUBLIC_DOMAIN in server-provisioning/.env.example). "+
				"Used as the WebAuthn relying-party ID; the passkey pairing factor stays disabled while this is empty. "+
				"Once a passkey is registered against it, changing this value invalidates every registered passkey.")
	)
	flag.Parse()

	// Secrets belong in the environment, not in argv. /proc/<pid>/cmdline is
	// world-readable, so a flag value is visible to every local account —
	// including the coding agents this daemon starts, which run as the same
	// user. /proc/<pid>/environ is 0400 and owner-only, which is not perfect
	// separation but is the difference between "every account on the box" and
	// "this uid and root".
	//
	// The flags stay for tests and for running the binary by hand; the
	// environment only fills in what a flag did not already set, so an explicit
	// flag still wins.
	envDefault(ownerEmail, "CCR_OWNER_EMAIL")
	envDefault(ownerPasswordHash, "CCR_OWNER_PASSWORD_HASH")
	envDefault(ownerTOTPSecret, "CCR_OWNER_TOTP_SECRET")
	envDefault(publicDomain, "CCR_PUBLIC_DOMAIN")
	envDefault(trustedProxies, "CCR_TRUSTED_PROXIES")

	if *setupOwner {
		if err := runSetupOwner(os.Stdin, os.Stdout, *ownerEmail); err != nil {
			log.Fatalf("setup-owner: %v", err)
		}
		return
	}

	// Never bind to a wildcard address. Public reachability is CloudGate's
	// job, and it forwards to a local port.
	if err := checkBindAddress(*addr); err != nil {
		log.Fatalf("refusing to start: %v", err)
	}

	// A malformed --public-domain is worth catching now, not the first time
	// a browser rejects a passkey ceremony against it. An IP in particular
	// looks "configured" right up until WebAuthn refuses it outright.
	if err := validatePublicDomain(*publicDomain); err != nil {
		log.Fatalf("invalid --public-domain: %v", err)
	}

	if *insecureNoAuth {
		// A non-loopback bind address plus disabled auth is not a test
		// configuration, it is an open shell on the network — refuse
		// outright rather than merely warning.
		if err := checkLoopbackOnly(*addr); err != nil {
			log.Fatalf("refusing to start with --insecure-no-auth: %v", err)
		}
		log.Print(strings.Repeat("!", 78))
		log.Print("!!! --insecure-no-auth is set: device authentication is DISABLED on /sessions and /files.")
		log.Print("!!! This is the clearly-marked test configuration CLAUDE.md permits, for the xterm.js")
		log.Print("!!! test client ONLY. It is refused on anything but a loopback bind address and is")
		log.Print("!!! never the default — do not use this in production.")
		log.Print(strings.Repeat("!", 78))
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
	termMux := mgr.Routes()

	// The file API serves only the allowlisted roots — there is deliberately
	// no browser over the whole filesystem (Section 8.1).
	fileStore, err := files.NewStore(splitRoots(*roots))
	if err != nil {
		log.Fatalf("file roots: %v", err)
	}
	uploader, err := files.NewUploader(fileStore, *uploadDir)
	if err != nil {
		log.Fatalf("upload directory: %v", err)
	}
	filesMux := http.NewServeMux()
	(&files.API{Store: fileStore, Uploader: uploader}).Register(filesMux)

	wired, err := newRouter(routerConfig{
		TermMux:           termMux,
		FilesMux:          filesMux,
		Database:          database,
		OwnerEmail:        *ownerEmail,
		OwnerPasswordHash: *ownerPasswordHash,
		OwnerTOTPSecret:   *ownerTOTPSecret,
		PublicDomain:      *publicDomain,
		TrustedProxies:    splitRoots(*trustedProxies),
		InsecureNoAuth:    *insecureNoAuth,
	})
	if err != nil {
		log.Fatalf("wire routes: %v", err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           wired.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout on purpose: a terminal WebSocket is long-lived and
		// a write deadline would kill idle sessions.
	}

	log.Printf("claudecode-remoted listening on %s (db=%s, locks=%s)", *addr, *dbPath, lockMgr.Dir())
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// envDefault fills *target from the named environment variable when the flag
// was left empty. Flag wins, so an explicit command line is never silently
// overridden by a stale variable in the shell.
func envDefault(target *string, key string) {
	if *target == "" {
		*target = os.Getenv(key)
	}
}

// routerConfig collects everything newRouter needs to assemble the
// daemon's HTTP surface. Split out from main() so tests can build the
// exact same wiring against a temporary database instead of a real
// listening server.
type routerConfig struct {
	// TermMux and FilesMux are the already-built, unauthenticated route
	// muxes from terminal.Manager.Routes() and files.API.Register — this
	// function's job is only to wrap them in device auth, not to build
	// them (that stays in main(), which owns the terminal/files-specific
	// setup).
	TermMux  *http.ServeMux
	FilesMux *http.ServeMux
	Database *db.DB

	OwnerEmail        string
	OwnerPasswordHash string
	OwnerTOTPSecret   string
	// PublicDomain is CCR_PUBLIC_DOMAIN / --public-domain. Empty leaves the
	// passkey pairing factor unsatisfiable (identity.NewWebAuthnVerifier's
	// fail-closed default) — see validatePublicDomain for the startup check
	// that keeps a malformed value from reaching here at all.
	PublicDomain string

	// TrustedProxies are the CIDRs whose forwarded-for headers the rate
	// limiter believes (identity.ClientIPResolver). Empty trusts none.
	TrustedProxies []string

	InsecureNoAuth bool
}

// wiredRouter is the assembled handler plus the identity components tests
// need direct access to (issuing challenges, granting step-up) without
// re-deriving them from cfg.
type wiredRouter struct {
	Handler    http.Handler
	DeviceAuth *identity.DeviceAuthenticator
	StepUp     *identity.StepUpGate
	Owner      *identity.Owner
}

// newRouter wires device authentication (G-02: the file API gets exactly
// the same Ed25519 challenge-response as the terminal WebSocket, applied
// as middleware — not a second, parallel scheme) and the /owner pairing
// routes (D-05: a route in this daemon) around the given sub-muxes.
func newRouter(cfg routerConfig) (wiredRouter, error) {
	identityStore := identity.NewStore(cfg.Database)
	deviceAuth := identity.NewDeviceAuthenticator(identityStore)
	stepUpGate := identity.NewStepUpGate()
	rateLimiter := identity.NewRateLimiter()

	// WebAuthnRPID is left empty (identity.NewWebAuthnVerifier's fail-closed
	// default) unless cfg.PublicDomain was set — D-04 is per-installation
	// config now, not a value this repo may guess at. WebAuthnOrigins is
	// derived from the same domain: CloudGate/Cloudflare terminates TLS at
	// the edge (server-provisioning/cloudgate/SETUP.md), so the origin a
	// browser actually sees is always https://<domain>, never the plaintext
	// address the daemon binds to locally.
	webAuthnOrigins := []string(nil)
	if cfg.PublicDomain != "" {
		webAuthnOrigins = []string{"https://" + cfg.PublicDomain}
	}
	owner := identity.NewOwner(identity.OwnerConfig{
		Email:               cfg.OwnerEmail,
		PasswordHash:        cfg.OwnerPasswordHash,
		TOTPSecret:          cfg.OwnerTOTPSecret,
		WebAuthnRPID:        cfg.PublicDomain,
		WebAuthnDisplayName: "ClaudeCode Remote",
		WebAuthnOrigins:     webAuthnOrigins,
		TrustedProxies:      cfg.TrustedProxies,
	}, identityStore, rateLimiter)

	wsTickets := newWSTicketStore()

	root := http.NewServeMux()

	// The file API and the terminal REST surface get exactly the same
	// device auth (G-02) — unless --insecure-no-auth was explicitly set
	// for the xterm.js test client, in which case both are mounted
	// unauthenticated exactly as they were before Phase 7.
	var authedTerm, authedFiles http.Handler = cfg.TermMux, cfg.FilesMux
	if !cfg.InsecureNoAuth {
		authedTerm = wrapSessionAuth(deviceAuth, wsTickets, cfg.TermMux)
		authedFiles = identity.RequireDevice(deviceAuth, cfg.FilesMux)
	}
	for _, pattern := range []string{"/sessions", "/sessions/"} {
		root.Handle(pattern, authedTerm)
	}
	for _, pattern := range []string{"/files", "/files/download", "/files/upload", "/files/upload/"} {
		root.Handle(pattern, authedFiles)
	}

	// /owner/pair/* must stay unauthenticated: pairing is how a device
	// becomes authenticated in the first place.
	owner.RegisterPairing(root)

	// Revoking a device is a Section 10.1 sensitive action: even an
	// already-paired device must not be able to do it on possession of its
	// everyday Ed25519 key alone.
	//
	// Listing devices is deliberately NOT behind step-up, and the two are
	// mounted separately rather than sharing one grant. Two reasons, both
	// concrete:
	//
	//  1. Section 10.1's catalogue is revoke, pair, delete backup, change
	//     security settings. Listing is a read of the owner's own devices,
	//     not a state change, and inventing a fifth entry would be adding to
	//     a list this project treats as binding.
	//  2. Step-up grants are single-use. Sharing one grant across both routes
	//     means the obvious flow — list the devices, then revoke one — spends
	//     the grant on the list and then fails the revoke. Anyone hitting that
	//     would reasonably conclude step-up is broken.
	ownerDeviceListMux := http.NewServeMux()
	ownerDeviceRevokeMux := http.NewServeMux()
	owner.RegisterDeviceManagement(ownerDeviceListMux)
	owner.RegisterDeviceManagement(ownerDeviceRevokeMux)

	root.Handle("/owner/devices", identity.RequireDevice(deviceAuth, ownerDeviceListMux))
	root.Handle("/owner/devices/revoke", identity.RequireDevice(deviceAuth,
		identity.RequireStepUp(stepUpGate, identity.ActionRevokeDevice, ownerDeviceRevokeMux)))

	// Lets an already-paired device sign the everyday Ed25519
	// challenge-response (Section 5.4). Unauthenticated by necessity — a
	// device has nothing to authenticate with yet at this step, exactly
	// like an SSH server issuing a challenge to a claimed key.
	root.HandleFunc("/auth/challenge", handleAuthChallenge(deviceAuth))
	// Trades an already-authenticated HTTP request for the short-lived,
	// single-use ticket the WebSocket handshake uses instead of headers
	// (see wsTicketStore's doc comment for why).
	root.Handle("/auth/ws-ticket", identity.RequireDevice(deviceAuth, handleAuthWSTicket(wsTickets)))

	root.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// A WebSocket that exists only to prove a WebSocket survives the tunnel.
	//
	// Plain HTTPS answering says nothing about whether a proxy strips Upgrade
	// headers, and a proxy that does looks perfectly healthy right up until the
	// first terminal is opened. The obvious check — upgrade against a real
	// session path — cannot work: session routes require a single-use ticket,
	// so the verifier gets a 401 before the handshake, and the only ways to
	// make that check pass would be to hand it a real ticket (needs a paired
	// device, which the operator does not have yet at this point) or to weaken
	// the session auth (never).
	//
	// So: its own route, unauthenticated, which accepts the upgrade, says
	// "ok", and closes. It reaches no session, reads nothing and starts
	// nothing, and the same-origin check still applies.
	root.HandleFunc("/health/ws", terminal.HandleHealthWS)

	// The xterm.js test client from Phase 4. It exists to validate the
	// protocol before the Flutter app is built, not as a shipping UI.
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return wiredRouter{}, fmt.Errorf("embedded web assets: %w", err)
	}
	root.Handle("/", http.FileServer(http.FS(sub)))

	handler := secureHeaders(limitRequestBody(root))

	return wiredRouter{Handler: handler, DeviceAuth: deviceAuth, StepUp: stepUpGate, Owner: owner}, nil
}

// maxJSONBody caps request bodies on everything except file uploads. Every
// other endpoint here takes a small JSON object; a megabyte is already orders
// of magnitude more than any of them need.
const maxJSONBody = 1 << 20

// limitRequestBody stops an unauthenticated caller from making the daemon
// allocate arbitrary memory by streaming an endless body at a JSON endpoint.
// `json.Decoder` reads until EOF, so without a cap "POST /auth/challenge" with
// an infinite body is a memory exhaustion primitive that needs no credentials.
//
// The upload path is deliberately exempt: it streams chunks straight to disk
// rather than buffering them, and capping it here would cap the file size for
// no benefit.
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/files/upload") {
			r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
		}
		next.ServeHTTP(w, r)
	})
}

// secureHeaders sets the response headers a browser needs to not do something
// unhelpful with our responses.
//
// The API returns JSON and the only HTML is the local test client, so these are
// cheap. The one that matters most is nosniff: without it a browser may decide
// a JSON error body containing attacker-influenced text is HTML and run it.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// The WebSocket ticket travels in the query string. Without this, a
		// click from the test client would leak it in a Referer header.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		// A strict CSP would break the test client, which loads xterm.js from a
		// CDN by design (it is a development tool, not a shipping UI). Scope the
		// policy to the API responses, where nothing should ever be executed.
		if !strings.HasPrefix(r.URL.Path, "/static") && r.URL.Path != "/" {
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		}

		next.ServeHTTP(w, r)
	})
}

// wrapSessionAuth applies device authentication to termMux. Every request
// is header-checked (identity.RequireDevice) except the WebSocket upgrade
// itself, "/sessions/{id}/stream" — a browser cannot set the
// X-Device-Id/X-Device-Signature headers on a WebSocket handshake, so that
// one path alone accepts a ticket from wsTicketStore instead. This mirrors
// the ambiguity terminal/http.go's own splitSessionPath already has (a
// session literally named "stream" would collide with the tail check
// there too) — not a new risk introduced here, since session ids are
// always daemon-generated hex, never "stream".
func wrapSessionAuth(auth *identity.DeviceAuthenticator, tickets *wsTicketStore, next http.Handler) http.Handler {
	headerChecked := identity.RequireDevice(auth, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stream") {
			ticket := r.URL.Query().Get("ticket")
			if ticket == "" || !tickets.consume(ticket) {
				writeJSONError(w, http.StatusUnauthorized, "missing or invalid ws ticket")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		headerChecked.ServeHTTP(w, r)
	})
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func handleAuthChallenge(auth *identity.DeviceAuthenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			DeviceID string `json:"device_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeviceID == "" {
			writeJSONError(w, http.StatusBadRequest, "device_id is required")
			return
		}
		nonce, err := auth.IssueChallenge(body.DeviceID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to issue challenge")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"challenge": base64.StdEncoding.EncodeToString(nonce),
		})
	}
}

func handleAuthWSTicket(tickets *wsTicketStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ticket, err := tickets.issue()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to issue ticket")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"ticket": ticket})
	}
}

// wsTicketTTL bounds how long an issued ticket stays usable. Seconds, not
// minutes: the client is expected to open the WebSocket immediately after
// requesting the ticket, and the shorter this window is, the less a copy
// sitting in a proxy access log or browser history is worth to whoever
// might read it later.
const wsTicketTTL = 30 * time.Second

// wsTicketStore issues short-lived, single-use tickets so a browser
// WebSocket handshake — which cannot set the X-Device-Id/X-Device-Signature
// headers RequireDevice checks, since browsers do not let script attach
// arbitrary headers to a WebSocket upgrade — can still prove device
// identity. The client authenticates over an ordinary HTTP request first
// (headers, like every other request), trades that for a ticket via
// /auth/ws-ticket, then passes the ticket as a query parameter on the
// handshake URL.
//
// A query parameter is normally the wrong place for a credential: it ends
// up in proxy access logs and browser history. It is acceptable here only
// because of two properties this ticket has that a device signature does
// not: it lives for wsTicketTTL (seconds), and it is destroyed the instant
// it is consumed — successfully or not — so a copy sitting in a log file
// is already worthless by the time anyone could read it back out.
//
// Safe for concurrent use.
type wsTicketStore struct {
	mu      sync.Mutex
	tickets map[string]time.Time // ticket -> expiry
	nowFunc func() time.Time
}

func newWSTicketStore() *wsTicketStore {
	return &wsTicketStore{tickets: make(map[string]time.Time), nowFunc: time.Now}
}

// SetClock overrides the clock, for tests.
func (s *wsTicketStore) SetClock(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nowFunc = f
}

func (s *wsTicketStore) issue() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate ws ticket: %w", err)
	}
	ticket := hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.tickets[ticket] = s.nowFunc().Add(wsTicketTTL)
	return ticket, nil
}

// consume reports whether ticket is currently valid, and deletes it
// either way: a ticket is destroyed on its first use whether that use
// succeeds or fails, the same reasoning as
// DeviceAuthenticator.Verify consuming a challenge unconditionally
// (identity/device.go) — a used or expired ticket must never be retried
// into validity.
func (s *wsTicketStore) consume(ticket string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.tickets[ticket]
	if ok {
		delete(s.tickets, ticket)
	}
	if !ok {
		return false
	}
	return s.nowFunc().Before(expiry)
}

func (s *wsTicketStore) sweepLocked() {
	now := s.nowFunc()
	for t, expiry := range s.tickets {
		if now.After(expiry) {
			delete(s.tickets, t)
		}
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

// checkLoopbackOnly is the extra restriction --insecure-no-auth adds on
// top of checkBindAddress: not just "not a wildcard" but actually
// loopback-only. Disabling device auth on anything reachable from beyond
// this machine — including the Tailscale interface checkBindAddress alone
// would still permit — is not a test configuration, it is an open shell on
// the network.
func checkLoopbackOnly(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	host = strings.TrimSpace(host)
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("--insecure-no-auth requires a loopback bind address (127.0.0.1, ::1, or localhost), got %q", host)
}

// hostnameRE matches a bare DNS hostname: one or more dot-separated labels
// (letters/digits/hyphens, not starting or ending with a hyphen) followed by
// an alphabetic TLD. Deliberately requires at least one dot — a public
// domain always has one — so a bare word like "localhost" is rejected here
// too, not just IPs and URLs.
var hostnameRE = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

// validatePublicDomain rejects anything that cannot serve as a WebAuthn
// relying-party ID. Empty is fine — that is the fail-closed default that
// keeps the passkey factor disabled (identity.NewWebAuthnVerifier). A
// non-empty value must be a bare hostname: no scheme, no port, no path, and
// never an IP address, since browsers reject an IP as an RP ID outright and
// that is a miserable thing to discover only after someone believes they
// have already configured it.
func validatePublicDomain(domain string) error {
	if domain == "" {
		return nil
	}
	if strings.Contains(domain, "://") {
		return fmt.Errorf("must be a bare hostname, not a URL: %q", domain)
	}
	if strings.ContainsAny(domain, "/:") {
		return fmt.Errorf("must not contain a path or port: %q", domain)
	}
	if net.ParseIP(domain) != nil {
		return fmt.Errorf("must be a domain name, not an IP address (WebAuthn relying-party IDs cannot be IPs): %q", domain)
	}
	if len(domain) > 253 || !hostnameRE.MatchString(domain) {
		return fmt.Errorf("does not look like a valid hostname: %q", domain)
	}
	return nil
}

var errWildcardBind = errors.New(
	"bind address must be localhost or a specific interface (e.g. the Tailscale IP), never a wildcard: " +
		"public access is CloudGate's job")
