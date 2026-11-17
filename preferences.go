package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type connectionFields struct {
	Host     string `json:"host,omitempty"`
	Port     string `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	StartDir string `json:"start_dir,omitempty"`
	KeyPath  string `json:"key_path,omitempty"`
}

func (c connectionFields) credentialScope() string {
	return strings.Join([]string{"Site Uploader", strings.ToLower(c.Host), c.Port, c.User, c.KeyPath}, "\x00")
}

type profile struct {
	Name string `json:"name"`
	connectionFields
	ProtectedSecret []byte `json:"protected_secret,omitempty"`
}

type preferences struct {
	connectionFields                   // last connection made without a profile
	Profiles         []profile         `json:"profiles"`
	LastProfile      string            `json:"last_profile,omitempty"`
	Fingerprints     map[string]string `json:"fingerprints"`
}

func (p preferences) profileIndex(name string) int {
	return slices.IndexFunc(p.Profiles, func(item profile) bool { return strings.EqualFold(item.Name, name) })
}

func preferencesPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Site Uploader", "settings.json"), nil
}

func loadPreferences() (preferences, error) {
	p := preferences{Profiles: []profile{}}
	name, err := preferencesPath()
	if err != nil {
		return p, err
	}
	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return decodePreferences(data)
}

func decodePreferences(data []byte) (preferences, error) {
	var p preferences
	if err := json.Unmarshal(data, &p); err != nil {
		return preferences{Profiles: []profile{}}, err
	}
	if p.Profiles == nil {
		p.Profiles = []profile{}
		if p.Host != "" && p.User != "" {
			p.LastProfile = p.Host
			p.Profiles = append(p.Profiles, profile{Name: p.Host, connectionFields: p.connectionFields})
			p.connectionFields = connectionFields{}
		}
	}
	return p, nil
}

func (p preferences) save() error {
	name, err := preferencesPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := errors.Join(enc.Encode(p), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}
