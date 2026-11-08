package panel

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestBcryptPasswordsUsesRandomSaltAndCost12(t *testing.T) {
	const password = "same-password-with-random-salt"
	hasher := BcryptPasswords{}
	first, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("hashing the same password twice reused the salt")
	}
	for _, hash := range []string{first, second} {
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil || cost != 12 {
			t.Errorf("bcrypt cost = %d, error %v; want 12", cost, err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
			t.Fatalf("hash does not verify the original password: %v", err)
		}
	}
}
