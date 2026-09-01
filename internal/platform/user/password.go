package user

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	iterations = 600_000
	saltBytes  = 16
	keyBytes   = 32
	scheme     = "pbkdf2-sha256"
)

var errBadStored = errors.New("the stored password cannot be read")

func hashPassword(password string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return derive(password, salt, iterations)
}

func derive(password string, salt []byte, rounds int) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, rounds, keyBytes)
	if err != nil {
		return "", fmt.Errorf("derive the password: %w", err)
	}

	return strings.Join([]string{
		scheme,
		strconv.Itoa(rounds),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

func matches(stored, password string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != scheme {
		return false, errBadStored
	}

	rounds, err := strconv.Atoi(parts[1])
	if err != nil || rounds <= 0 {
		return false, errBadStored
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false, errBadStored
	}

	again, err := derive(password, salt, rounds)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(again), []byte(stored)) == 1, nil
}

var decoy = mustDecoy()

func mustDecoy() string {
	stored, err := hashPassword("there is no account with this address")
	if err != nil {
		panic("cannot build the decoy password: " + err.Error())
	}
	return stored
}

func spendTheSameTime(password string) {
	_, _ = matches(decoy, password)
}
