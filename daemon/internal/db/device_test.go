package db

import (
	"database/sql"
	"errors"
	"testing"
)

func mustPair(t *testing.T, d *DB, id string) {
	t.Helper()
	if err := d.PairDevice(Device{ID: id, Name: "phone", Platform: "android", PubKey: "pk-" + id}); err != nil {
		t.Fatalf("PairDevice(%s): %v", id, err)
	}
}

func TestPairAndGetDevice(t *testing.T) {
	d := openTemp(t)
	mustPair(t, d, "dev1")

	got, err := d.GetDevice("dev1")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.PubKey != "pk-dev1" || got.Platform != "android" {
		t.Errorf("unexpected device: %+v", got)
	}
	if got.Revoked {
		t.Error("a freshly paired device must not be revoked")
	}
	if got.PairedAt == 0 || got.LastSeenAt == 0 {
		t.Errorf("timestamps not defaulted: %+v", got)
	}
}

// Revocation has to take effect immediately, not at the next pairing, so the
// revoked flag must be readable straight after the update.
func TestRevokeDevice(t *testing.T) {
	d := openTemp(t)
	mustPair(t, d, "dev1")

	if err := d.RevokeDevice("dev1"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	got, err := d.GetDevice("dev1")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if !got.Revoked {
		t.Fatal("device is not marked revoked after RevokeDevice")
	}
}

// The row is deliberately kept, not deleted: a deleted id could be paired
// again and silently inherit the revoked device's trust.
func TestRevokedDeviceIsKeptNotDeleted(t *testing.T) {
	d := openTemp(t)
	mustPair(t, d, "dev1")
	if err := d.RevokeDevice("dev1"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	list, err := d.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the revoked device to still be listed, got %d rows", len(list))
	}

	// And the id must not be re-pairable, or the whole point is lost.
	if err := d.PairDevice(Device{ID: "dev1", PubKey: "attacker-key"}); err == nil {
		t.Fatal("a revoked device id was allowed to be paired again")
	}
}

func TestRevokeUnknownDeviceReportsNoRows(t *testing.T) {
	d := openTemp(t)
	if err := d.RevokeDevice("nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("got %v, want sql.ErrNoRows", err)
	}
}

func TestGetUnknownDeviceReportsNoRows(t *testing.T) {
	d := openTemp(t)
	if _, err := d.GetDevice("nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("got %v, want sql.ErrNoRows", err)
	}
}

func TestTouchDevice(t *testing.T) {
	d := openTemp(t)
	mustPair(t, d, "dev1")
	if err := d.TouchDevice("dev1"); err != nil {
		t.Fatalf("TouchDevice: %v", err)
	}
}
