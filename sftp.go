package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type connectionOptions struct {
	address, user, secret, keyPath string
}

type unknownHost struct{ fingerprint string }

func (e *unknownHost) Error() string {
	return "Неизвестный ключ сервера: " + e.fingerprint
}

type remote struct {
	*sftp.Client
	transport *ssh.Client
	socket    net.Conn
}

func connect(o connectionOptions, fingerprint string) (*remote, error) {
	var auth ssh.AuthMethod
	if o.keyPath != "" {
		key, err := os.ReadFile(o.keyPath)
		if err != nil {
			return nil, fmt.Errorf("чтение ключа: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		var encrypted *ssh.PassphraseMissingError
		if errors.As(err, &encrypted) && o.secret != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(o.secret))
		}
		if err != nil {
			return nil, fmt.Errorf("не удалось открыть SSH-ключ (проверьте формат и пароль ключа): %w", err)
		}
		auth = ssh.PublicKeys(signer)
	} else {
		auth = ssh.Password(o.secret)
	}

	config := &ssh.ClientConfig{
		User: o.user,
		Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			actual := ssh.FingerprintSHA256(key)
			if fingerprint == "" {
				return &unknownHost{actual}
			}
			if fingerprint != actual {
				return fmt.Errorf("ключ сервера изменился. Ожидался %s, получен %s. Соединение прервано", fingerprint, actual)
			}
			return nil
		},
	}
	conn, err := net.DialTimeout("tcp", o.address, 12*time.Second)
	if err != nil {
		return nil, fmt.Errorf("подключение к %s: %w", o.address, err)
	}
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	sshConn, channels, requests, err := ssh.NewClientConn(conn, o.address, config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	transport := ssh.NewClient(sshConn, channels, requests)
	client, err := sftp.NewClient(transport)
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("не удалось запустить SFTP: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return &remote{client, transport, conn}, nil
}

func (r *remote) close() {
	_ = r.transport.Close()
	_ = r.Client.Close()
}

func (r *remote) list(dir string) (string, []os.FileInfo, error) {
	if dir == "" {
		dir = "."
	}
	actual, err := r.RealPath(dir)
	if err != nil {
		return "", nil, err
	}
	items, err := r.ReadDir(actual)
	if err != nil {
		return "", nil, err
	}
	slices.SortFunc(items, func(a, b os.FileInfo) int {
		if a.IsDir() != b.IsDir() {
			if a.IsDir() {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name()))
	})
	return actual, items, nil
}

func validName(name string) bool {
	return strings.TrimSpace(name) != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00\r\n")
}

func (r *remote) existing(target string) (os.FileInfo, error) {
	info, err := r.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return info, err
}

func (r *remote) move(from, to string) error {
	if from == to {
		return nil
	}
	if old, err := r.existing(to); err != nil {
		return err
	} else if old != nil {
		return errors.New("файл или папка с таким именем уже существует")
	}
	return r.Rename(from, to)
}

// Write beside the target first; a failed transfer leaves the old file intact.
func (r *remote) writeFile(target string, src io.Reader, replace os.FileInfo) error {
	if replace != nil {
		if !replace.Mode().IsRegular() {
			return errors.New("можно заменить только обычный файл")
		}
		if _, ok := r.HasExtension("posix-rename@openssh.com"); !ok {
			return errors.New("сервер не поддерживает безопасную замену файла (posix-rename@openssh.com)")
		}
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(target), fmt.Sprintf(".siteuploader-%x", nonce))
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = r.Remove(tmp)
		}
	}()
	// Restrict access during the copy, then restore the destination mode if replacing.
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	_, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if replace != nil {
		mode = replace.Mode().Perm()
	}
	if err := r.Chmod(tmp, mode); err != nil {
		return err
	}
	if replace == nil {
		err = r.move(tmp, target)
	} else {
		info, checkErr := r.Lstat(target)
		if checkErr != nil {
			return checkErr
		}
		if !info.Mode().IsRegular() || info.Size() != replace.Size() || !info.ModTime().Equal(replace.ModTime()) || info.Mode() != replace.Mode() {
			return errors.New("файл изменился на сервере во время операции")
		}
		err = r.PosixRename(tmp, target)
	}
	committed = err == nil
	return err
}

func (r *remote) download(source, target string) error {
	info, err := r.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("скачивание доступно только для обычных файлов")
	}
	f, err := r.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".siteuploader-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, copyErr := io.Copy(tmp, f)
	closeErr := tmp.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

const maxTextSize = 1 << 20

func (r *remote) readText(name string) ([]byte, error) {
	info, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxTextSize {
		return nil, errors.New("редактор открывает только обычные текстовые файлы размером до 1 МБ")
	}
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTextSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTextSize || !utf8.Valid(data) || bytes.ContainsAny(data, "\x00") {
		return nil, errors.New("редактор поддерживает текст UTF-8 размером до 1 МБ")
	}
	return data, nil
}

func (r *remote) saveText(name string, original [32]byte, data []byte) error {
	if len(data) > maxTextSize {
		return errors.New("размер текста превышает 1 МБ")
	}
	info, err := r.Lstat(name)
	if err != nil {
		return err
	}
	current, err := r.readText(name)
	if err != nil {
		return err
	}
	if sha256.Sum256(current) != original {
		return errors.New("файл уже изменился на сервере. Откройте его заново перед редактированием")
	}
	return r.writeFile(name, bytes.NewReader(data), info)
}
