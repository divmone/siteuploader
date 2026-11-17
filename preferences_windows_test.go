package main

import (
	"bytes"
	"os"
	"testing"
)

func TestSavedProfiles(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	old := []byte(`{"host":"site.example","port":"22","user":"admin","start_dir":"/var/www/site","key_path":"id_ed25519","fingerprints":{"site.example:22":"SHA256:test"}}`)
	p, err := decodePreferences(old)
	must(t, err)
	if len(p.Profiles) != 1 || p.LastProfile != "site.example" || p.Host != "" || p.Profiles[0].StartDir != "/var/www/site" || p.Profiles[0].KeyPath != "id_ed25519" {
		t.Fatalf("legacy connection not migrated correctly: %+v", p)
	}
	second := profile{Name: "Второй сайт", connectionFields: connectionFields{Host: "second.example", Port: "2222", User: "web", StartDir: "/web"}}
	const password = "test secret пароль 687132"
	second.ProtectedSecret, err = protectSecret(password, second.credentialScope())
	must(t, err)
	p.Profiles = append(p.Profiles, second)
	p.LastProfile = second.Name
	must(t, p.save())
	name, err := preferencesPath()
	must(t, err)
	raw, err := os.ReadFile(name)
	must(t, err)
	if bytes.Contains(raw, []byte(password)) {
		t.Fatal("password stored in plaintext")
	}
	loaded, err := loadPreferences()
	must(t, err)
	if len(loaded.Profiles) != 2 || loaded.LastProfile != second.Name || loaded.Fingerprints["site.example:22"] != "SHA256:test" {
		t.Fatalf("profiles or fingerprints lost after reload: %+v", loaded)
	}
	second = loaded.Profiles[1]
	secret, err := unprotectSecret(second.ProtectedSecret, second.credentialScope())
	must(t, err)
	if secret != password {
		t.Fatal("saved password did not round-trip")
	}
	if _, err := unprotectSecret(second.ProtectedSecret, loaded.Profiles[0].credentialScope()); err == nil {
		t.Fatal("saved password can be decrypted for a different connection")
	}
	loaded.connectionFields = loaded.Profiles[0].connectionFields
	loaded.Profiles = loaded.Profiles[:0]
	loaded.LastProfile = ""
	must(t, loaded.save())
	loaded, err = loadPreferences()
	must(t, err)
	if len(loaded.Profiles) != 0 || loaded.Host != "site.example" {
		t.Fatalf("deleting all profiles should preserve only the ad hoc connection: %+v", loaded)
	}
}
