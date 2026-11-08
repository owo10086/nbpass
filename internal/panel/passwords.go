package panel

import "golang.org/x/crypto/bcrypt"

type BcryptPasswords struct{ Cost int }

func (b BcryptPasswords) Hash(password string) (string, error) {
	cost := b.Cost
	if cost == 0 {
		cost = 12
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	return string(hash), err
}

func (BcryptPasswords) Verify(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
