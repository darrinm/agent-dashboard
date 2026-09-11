//go:build !darwin || !cgo

package core

import "errors"

func saveCredential(dir, token string) (string, error) { return "", nil }
func loadCredential(account string) (string, error) {
	return "", errors.New("this build cannot read macOS Keychain credentials")
}
