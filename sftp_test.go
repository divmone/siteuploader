package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestSFTPWorkflow(t *testing.T) {
	root := t.TempDir()
	remoteRoot := filepath.ToSlash(root)
	if !strings.HasPrefix(remoteRoot, "/") {
		remoteRoot = "/" + remoteRoot
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	host, err := ssh.NewSignerFromKey(hostKey)
	must(t, err)
	_, userKey, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	userSigner, err := ssh.NewSignerFromKey(userKey)
	must(t, err)
	server := &ssh.ServerConfig{
		PasswordCallback: func(m ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if m.User() == "test" && string(pass) == "test-password" {
				return nil, nil
			}
			return nil, errors.New("invalid test credentials")
		},
		PublicKeyCallback: func(m ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if m.User() == "test" && bytes.Equal(key.Marshal(), userSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, errors.New("invalid test credentials")
		},
	}
	server.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveTestSFTP(conn, server)
		}
	}()

	o := connectionOptions{address: listener.Addr().String(), user: "test", secret: "test-password"}
	pin := ssh.FingerprintSHA256(host.PublicKey())
	_, err = connect(o, "")
	var unknown *unknownHost
	if !errors.As(err, &unknown) || unknown.fingerprint != pin {
		t.Fatalf("expected untrusted key %s, got %v", pin, err)
	}
	if _, err = connect(o, "SHA256:wrong"); err == nil || !strings.Contains(err.Error(), "ключ сервера изменился") {
		t.Fatalf("expected connection to reject the wrong fingerprint, got %v", err)
	}
	badAuth := o
	badAuth.secret = "wrong"
	if _, err := connect(badAuth, pin); err == nil {
		t.Fatal("incorrect password was accepted")
	}
	r, err := connect(o, pin)
	must(t, err)
	t.Cleanup(r.close)
	_ = r.socket.SetDeadline(time.Now().Add(10 * time.Second))
	dir, items, err := r.list(remoteRoot)
	must(t, err)
	if len(items) != 0 || !path.IsAbs(dir) {
		t.Fatalf("expected empty remote directory, got %q with %d entries", dir, len(items))
	}
	child := path.Join(dir, "test-folder")
	must(t, r.Mkdir(child))
	file := path.Join(dir, "файл.txt")
	initial := "первая строка\nвторая строка\n"
	must(t, r.writeFile(file, strings.NewReader(initial), nil))
	_, items, err = r.list(dir)
	must(t, err)
	if len(items) != 2 || !items[0].IsDir() || items[1].Name() != "файл.txt" {
		t.Fatalf("unexpected directory listing: %+v", items)
	}
	assertText := func(name, want string) {
		t.Helper()
		data, err := r.readText(name)
		must(t, err)
		if string(data) != want {
			t.Fatalf("%s contains %q; expected %q", name, data, want)
		}
	}
	assertText(file, initial)
	old, err := r.Lstat(file)
	must(t, err)
	if err := r.writeFile(file, brokenReader{}, old); err == nil {
		t.Fatal("failed upload reported success")
	}
	if _, items, err := r.list(dir); err != nil || len(items) != 2 {
		t.Fatalf("failed upload left temp files: %v, %v", items, err)
	}
	assertText(file, initial)
	originalDigest := sha256.Sum256([]byte(initial))
	const edited = "файл изменён\n"
	must(t, r.saveText(file, originalDigest, []byte(edited)))
	assertText(file, edited)
	if err := r.saveText(file, originalDigest, []byte("stale draft")); err == nil {
		t.Fatal("stale edit overwrote the new contents")
	}
	if err := r.writeFile(file, strings.NewReader("collision"), nil); err == nil {
		t.Fatal("upload without overwrite approval replaced an existing file")
	}
	newPath := path.Join(child, "renamed.txt")
	must(t, r.move(file, newPath))
	if err := r.RemoveDirectory(child); err == nil {
		t.Fatal("deletion removed a directory that was not empty")
	}
	download := filepath.Join(t.TempDir(), "download.txt")
	must(t, os.WriteFile(download, []byte("old local file"), 0600))
	must(t, r.download(newPath, download))
	got, err := os.ReadFile(download)
	must(t, err)
	if string(got) != edited {
		t.Fatalf("download contained %q; expected %q", got, edited)
	}
	must(t, r.Remove(newPath))
	must(t, r.RemoveDirectory(child))
	_, items, err = r.list(dir)
	must(t, err)
	if len(items) != 0 {
		t.Fatalf("expected empty directory after deletion, got %v", items)
	}

	block, err := ssh.MarshalPrivateKeyWithPassphrase(userKey, "test", []byte("test-passphrase"))
	must(t, err)
	o.keyPath = filepath.Join(t.TempDir(), "id_ed25519")
	must(t, os.WriteFile(o.keyPath, pem.EncodeToMemory(block), 0600))
	o.secret = "test-passphrase"
	keyConnection, err := connect(o, pin)
	must(t, err)
	keyConnection.close()
}

func serveTestSFTP(socket net.Conn, cfg *ssh.ServerConfig) {
	defer socket.Close()
	_ = socket.SetDeadline(time.Now().Add(15 * time.Second))
	conn, channels, requests, err := ssh.NewServerConn(socket, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(requests)
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(ssh.UnknownChannelType, "test server only accepts sessions")
			continue
		}
		ch, reqs, err := incoming.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		for req := range reqs {
			var subsystem struct{ Name string }
			ok := req.Type == "subsystem" && ssh.Unmarshal(req.Payload, &subsystem) == nil && subsystem.Name == "sftp"
			_ = req.Reply(ok, nil)
			if ok {
				s, err := sftp.NewServer(ch)
				if err == nil {
					defer s.Close()
					_ = s.Serve()
				}
				return
			}
		}
	}
}

type brokenReader struct{}

func (brokenReader) Read(p []byte) (int, error) {
	return copy(p, "partial bytes"), fmt.Errorf("test interruption")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
