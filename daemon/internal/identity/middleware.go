// middleware.go wires DeviceAuthenticator (device.go) and StepUpGate
// (stepup.go) into standard net/http middleware.
//
// This resolves gap G-02 from docs/ARCHITECTURE.md: the file API
// (files/http.go) is supposed to get exactly the same device authentication
// as the terminal WebSocket (terminal/http.go), not a second parallel
// scheme. Both packages currently mount their routes on a plain
// *http.ServeMux with no auth at all — see the comments at the top of
// terminal/http.go and files/http.go. Neither of those files imports this
// package or needs to change: the caller wires auth on the outside by
// wrapping the mux each package already returns, e.g.:
//
//	termMux := terminalMgr.Routes()
//	filesAPI.Register(termMux) // or a second mux, merged however cmd/ prefers
//	handler := identity.RequireDevice(auth, termMux)
//
// One middleware, applied once, covers both surfaces identically.
package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
)

type contextKey int

const deviceIDKey contextKey = iota

// DeviceIDFromContext returns the device id RequireDevice attached to the
// request context, and whether one was present. Any handler downstream of
// RequireDevice can rely on ok being true.
func DeviceIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(deviceIDKey).(string)
	return id, ok
}

// writeAuthError matches the {"error": "..."} response shape the existing
// handlers already use (writeError in terminal/http.go, writeErr in
// files/http.go), so a client sees one consistent error envelope regardless
// of which layer rejected the request.
func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// RequireDevice returns middleware enforcing the Ed25519 challenge-response
// from Section 5.4 on every request to next. It expects two headers:
//
//	X-Device-Id:        the paired device's id
//	X-Device-Signature: base64 signature over that device's outstanding
//	                    challenge (see DeviceAuthenticator.IssueChallenge)
//
// A missing header, a malformed (non-base64) signature, or a signature that
// fails DeviceAuthenticator.Verify all produce the same 401 and — critically
// — never call next. Falling through to the wrapped handler on any of these
// would silently turn off auth for exactly the requests that supplied none,
// which is the one failure mode this middleware exists to rule out.
func RequireDevice(auth *DeviceAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.Header.Get("X-Device-Id")
		sigHeader := r.Header.Get("X-Device-Signature")
		if deviceID == "" || sigHeader == "" {
			writeAuthError(w, http.StatusUnauthorized, "missing device credentials")
			return
		}
		sig, err := base64.StdEncoding.DecodeString(sigHeader)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "malformed device signature")
			return
		}
		if err := auth.Verify(deviceID, sig); err != nil {
			// Deliberately the same message for every Verify failure —
			// unknown device, revoked device, bad signature, or expired
			// challenge — so this layer doesn't reopen the enumeration gap
			// Verify itself closes.
			writeAuthError(w, http.StatusUnauthorized, "device authentication failed")
			return
		}
		ctx := context.WithValue(r.Context(), deviceIDKey, deviceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireStepUp returns middleware additionally requiring a valid, unused
// step-up grant (Section 10.1) for action before next runs. Compose it
// inside RequireDevice, so the device id is already in context by the time
// this reads it:
//
//	handler := identity.RequireDevice(auth,
//	    identity.RequireStepUp(gate, identity.ActionRevokeDevice, revokeHandler))
//
// If no device id is in context — RequireDevice was skipped or a caller
// wired this middleware standalone — there is nothing to check a grant
// against, and the request is refused rather than treated as exempt.
func RequireStepUp(gate *StepUpGate, action SensitiveAction, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deviceID, ok := DeviceIDFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "device authentication required")
			return
		}
		if !RequiresStepUp(action) {
			next.ServeHTTP(w, r)
			return
		}
		if err := gate.Consume(deviceID, action); err != nil {
			writeAuthError(w, http.StatusUnauthorized, "step-up authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
