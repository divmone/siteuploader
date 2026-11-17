package main

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/windows"
)

func protectSecret(secret, scope string) ([]byte, error) {
	plain := []byte(secret)
	defer clear(plain)
	in, entropy := dataBlob(plain), dataBlob([]byte(scope))
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return bytes.Clone(unsafe.Slice(out.Data, out.Size)), nil
}

func unprotectSecret(ciphertext []byte, scope string) (string, error) {
	in, entropy := dataBlob(ciphertext), dataBlob([]byte(scope))
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := unsafe.Slice(out.Data, out.Size)
	defer clear(plain)
	return string(plain), nil
}

func dataBlob(data []byte) windows.DataBlob {
	return windows.DataBlob{Size: uint32(len(data)), Data: unsafe.SliceData(data)}
}
