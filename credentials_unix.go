//go:build darwin || linux

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const credentialService = "Site Uploader"
const credentialAccount = "profile encryption key"

func profileCipher(create bool) (cipher.AEAD, error) {
	encoded, err := keyring.Get(credentialService, credentialAccount)
	if create && errors.Is(err, keyring.ErrNotFound) {
		key := make([]byte, 32)
		defer clear(key)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		encoded = base64.StdEncoding.EncodeToString(key)
		err = keyring.Set(credentialService, credentialAccount, encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("системное хранилище паролей недоступно: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("повреждён ключ защиты паролей")
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func protectSecret(secret, scope string) ([]byte, error) {
	box, err := profileCipher(true)
	if err != nil {
		return nil, err
	}
	plain := []byte(secret)
	defer clear(plain)
	return box.Seal(nil, nil, plain, []byte(scope)), nil
}

func unprotectSecret(data []byte, scope string) (string, error) {
	box, err := profileCipher(false)
	if err != nil {
		return "", err
	}
	plain, err := box.Open(nil, nil, data, []byte(scope))
	if err != nil {
		return "", err
	}
	defer clear(plain)
	return string(plain), nil
}
