// stepup_http.go is the ceremony that issues step-up grants.
//
// stepup.go tracks grants and enforces that they are narrow and single-use; it
// deliberately performs no authentication of its own. Until this file existed
// nothing performed that authentication either, so no production code path ever
// called StepUpGate.Grant — only tests did. Every route behind RequireStepUp
// was therefore permanently unreachable, and the one route behind it is device
// revocation.
//
// That is not a locked door, it is a missing one: an owner whose phone is
// stolen could not revoke that phone's key through the shipped system at all.
// Between "revocation is guarded by a ceremony" and "revocation is impossible",
// the second is worse — the first is the security control, the second is the
// absence of the feature the control was protecting.
package identity

import (
	"encoding/json"
	"net/http"
	"strings"
)

// StepUpConfig wires the step-up endpoint to the owner credentials.
type StepUpConfig struct {
	Gate     *StepUpGate
	Limiter  *RateLimiter
	Resolver *ClientIPResolver

	Email        string
	PasswordHash string
	TOTPSecret   string
}

// StepUpHandler builds the POST /owner/step-up handler.
//
// It must be mounted INSIDE RequireDevice: the grant is bound to a device id,
// and the only trustworthy source of that id is the Ed25519 challenge-response
// the middleware already performed. A device id taken from the request body
// would let any caller mint a grant for someone else's device.
//
// The ceremony is the owner's password plus a TOTP code, re-entered now. Not
// the passkey: the passkey factor is unsatisfiable on every installation today
// (webauthn.go has no registration path and no credential storage), so binding
// step-up to it would leave revocation exactly as unreachable as it already
// was. Two freshly re-entered factors on top of a proven device key is a real
// re-authentication, and it is one that can actually be performed.
//
// ponytail: password + TOTP, not a fourth mechanism invented for this. When
// passkey registration lands, add the assertion as an accepted alternative here
// rather than replacing this — an owner whose passkey is on a lost phone still
// needs to be able to revoke that phone.
func StepUpHandler(cfg StepUpConfig) http.HandlerFunc {
	var passwordVerifier *PasswordVerifier
	if cfg.PasswordHash != "" {
		passwordVerifier = NewPasswordVerifier(cfg.PasswordHash)
	}
	var totpVerifier *TOTPVerifier
	if cfg.TOTPSecret != "" {
		totpVerifier = NewTOTPVerifier(cfg.TOTPSecret)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		deviceID, ok := DeviceIDFromContext(r.Context())
		if !ok {
			// Mounted outside RequireDevice. Refuse rather than fall back to
			// anything in the body — there is no device identity to bind a
			// grant to, so there is no grant that could be safely issued.
			writeAuthError(w, http.StatusUnauthorized, "device authentication required")
			return
		}

		var body struct {
			Action   string `json:"action"`
			Email    string `json:"email"`
			Password string `json:"password"`
			Code     string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		action := SensitiveAction(body.Action)
		if _, known := stepUpRegistry[action]; !known {
			// Only the registered actions, and no default. Issuing a grant for
			// an arbitrary string would put an unbounded, attacker-chosen key
			// into the gate's map, and would hand out grants for actions that
			// do not exist yet.
			writeAuthError(w, http.StatusBadRequest, "unknown action")
			return
		}

		ip := cfg.Resolver.IP(r)
		if err := cfg.Limiter.Allow(ip, cfg.Email); err != nil {
			writeAuthError(w, http.StatusTooManyRequests, "too many failed attempts")
			return
		}

		// Fail closed when the factors are not configured. An installation
		// without owner credentials must not get a free grant because there
		// was nothing to check against.
		if passwordVerifier == nil || totpVerifier == nil || cfg.Email == "" {
			writeAuthError(w, http.StatusUnauthorized, "step-up is not available: owner credentials are not configured")
			return
		}

		// A wrong email costs the IP only, never the account — same reasoning
		// as handlePairPassword: with a single account, charging unnamed
		// guesses to it turns the lockout into a denial of service anyone can
		// run against the owner.
		if !strings.EqualFold(body.Email, cfg.Email) {
			cfg.Limiter.RecordIPFailure(ip)
			writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		// Both factors are checked every time, and the result is one
		// indistinguishable error. Returning early on a bad password would say
		// which factor failed.
		passwordErr := passwordVerifier.Verify([]byte(body.Password))
		totpErr := totpVerifier.Verify([]byte(body.Code))
		if passwordErr != nil || totpErr != nil {
			cfg.Limiter.RecordFailure(ip, cfg.Email)
			writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		cfg.Limiter.RecordSuccess(ip, cfg.Email)
		cfg.Gate.Grant(deviceID, action)

		writeAuthJSON(w, http.StatusOK, map[string]any{
			"action":        string(action),
			"expires_in_s":  int(StepUpTTL.Seconds()),
			"single_use":    true,
			"bound_to_this": "device",
		})
	}
}

func writeAuthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
