package identity

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Elias02345/remote-agent/daemon/internal/db"
)

func openTempStore(t *testing.T) (*Store, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return NewStore(database), database
}

func TestStoreDevice_ValidKeyRoundTrips(t *testing.T) {
	store, database := openTempStore(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	if err := database.PairDevice(db.Device{ID: "dev-1", PubKey: base64.StdEncoding.EncodeToString(pub)}); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}

	gotPub, revoked, ok := store.Device("dev-1")
	if !ok {
		t.Fatal("Device() ok = false for a valid, existing device")
	}
	if revoked {
		t.Fatal("freshly paired device reported revoked")
	}
	if !gotPub.Equal(pub) {
		t.Fatalf("Device() pubkey = %x, want %x", gotPub, pub)
	}
}

func TestStoreDevice_UnknownIDReportsAbsent(t *testing.T) {
	store, _ := openTempStore(t)
	if _, _, ok := store.Device("nope"); ok {
		t.Fatal("Device() ok = true for an id that was never paired")
	}
}

// A malformed pubkey column must fail authentication like an unknown
// device, never be handed to ed25519.Verify as something that might match.
func TestStoreDevice_MalformedBase64FailsClosed(t *testing.T) {
	store, database := openTempStore(t)
	if err := database.PairDevice(db.Device{ID: "dev-1", PubKey: "not valid base64 !!"}); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}
	if _, _, ok := store.Device("dev-1"); ok {
		t.Fatal("Device() ok = true for a pubkey that does not even decode as base64")
	}
}

func TestStoreDevice_WrongLengthKeyFailsClosed(t *testing.T) {
	store, database := openTempStore(t)
	short := base64.StdEncoding.EncodeToString([]byte("too short for ed25519"))
	if err := database.PairDevice(db.Device{ID: "dev-1", PubKey: short}); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}
	if _, _, ok := store.Device("dev-1"); ok {
		t.Fatal("Device() ok = true for a pubkey of the wrong length")
	}
}

func TestStoreDevice_RevokedFlagPropagates(t *testing.T) {
	store, database := openTempStore(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	if err := database.PairDevice(db.Device{ID: "dev-1", PubKey: base64.StdEncoding.EncodeToString(pub)}); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}
	if err := database.RevokeDevice("dev-1"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	_, revoked, ok := store.Device("dev-1")
	if !ok || !revoked {
		t.Fatalf("Device() = (revoked=%v, ok=%v), want (true, true)", revoked, ok)
	}
}

func TestStorePairListRevokeDevices(t *testing.T) {
	store, _ := openTempStore(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	if err := store.PairDevice("dev-1", "phone", "android", base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}

	list, err := store.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(list) != 1 || list[0].ID != "dev-1" || list[0].Platform != "android" {
		t.Fatalf("ListDevices = %+v, want one dev-1/android entry", list)
	}

	if err := store.RevokeDevice("dev-1"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	list, err = store.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices after revoke: %v", err)
	}
	if len(list) != 1 || !list[0].Revoked {
		t.Fatalf("ListDevices after revoke = %+v, want revoked=true", list)
	}
}

func TestStoreRevokeDevice_UnknownIDReportsNoRows(t *testing.T) {
	store, _ := openTempStore(t)
	if err := store.RevokeDevice("nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("RevokeDevice(unknown) = %v, want sql.ErrNoRows", err)
	}
}
