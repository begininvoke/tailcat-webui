package secrets

import (
	"bytes"
	"testing"
)

func TestBoxRoundTripAndAssociatedData(t *testing.T) {
	box, err := NewBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := box.Seal([]byte("nodepriv:test"), "owner/server")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := box.Open(ciphertext, "owner/server")
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "nodepriv:test" {
		t.Fatalf("round trip = %q", plaintext)
	}
	if _, err := box.Open(ciphertext, "another owner"); err == nil {
		t.Fatal("Open accepted wrong associated data")
	}
}
