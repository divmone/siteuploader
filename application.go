package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type application struct {
	host, token  string
	stop         func()
	mu           sync.Mutex
	prefs        preferences
	prefsErr     error
	remote       *remote
	address, dir string
	files        []fileEntry
	download     downloadTicket
}

type fileEntry struct {
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type profileView struct {
	Name string `json:"name"`
	connectionFields
	Saved bool `json:"saved"`
}

type appState struct {
	Profiles       []profileView    `json:"profiles"`
	LastProfile    string           `json:"last_profile"`
	LastConnection connectionFields `json:"last_connection"`
	Address        string           `json:"address"`
	Directory      string           `json:"directory"`
	Files          []fileEntry      `json:"files"`
	Warning        string           `json:"warning,omitempty"`
}

type action struct {
	connectionFields
	Name        string `json:"name"`
	Profile     string `json:"profile"`
	Secret      string `json:"secret"`
	Remember    bool   `json:"remember"`
	Fingerprint string `json:"fingerprint"`
	Path        string `json:"path"`
	Text        string `json:"text"`
	Digest      string `json:"digest"`
}

type downloadTicket struct {
	key, file string
	expires   time.Time
}

type transferReader struct {
	io.Reader
	socket net.Conn
}

func (r transferReader) Read(p []byte) (int, error) {
	_ = r.socket.SetDeadline(time.Now().Add(90 * time.Second))
	return r.Reader.Read(p)
}

func (a *application) handler(static http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != a.host {
			fail(w, http.StatusForbidden, errors.New("неверный адрес приложения"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+a.host {
			fail(w, http.StatusForbidden, errors.New("недопустимый источник запроса"))
			return
		}
		switch {
		case r.URL.Path == "/download" && r.Method == http.MethodGet:
			a.serveDownload(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Site-Uploader")), []byte(a.token)) != 1 {
				fail(w, http.StatusForbidden, errors.New("откройте приложение по ссылке запуска"))
				return
			}
			if !a.mu.TryLock() {
				fail(w, http.StatusConflict, errors.New("дождитесь завершения текущей операции"))
				return
			}
			defer a.mu.Unlock()
			a.api(w, r, strings.TrimPrefix(r.URL.Path, "/api/"))
		case r.Method == http.MethodGet:
			static.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func send(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, err error) {
	send(w, status, map[string]string{"error": err.Error()})
}

func (a *application) state() appState {
	state := appState{
		Profiles: make([]profileView, 0, len(a.prefs.Profiles)), LastProfile: a.prefs.LastProfile,
		LastConnection: a.prefs.connectionFields, Address: a.address, Directory: a.dir, Files: a.files,
	}
	for _, item := range a.prefs.Profiles {
		state.Profiles = append(state.Profiles, profileView{item.Name, item.connectionFields, len(item.ProtectedSecret) != 0})
	}
	if a.prefsErr != nil {
		state.Warning = "Не удалось прочитать настройки: " + a.prefsErr.Error()
	}
	return state
}

func (a *application) save(p preferences) error {
	if err := p.save(); err != nil {
		return fmt.Errorf("не удалось сохранить настройки: %w", err)
	}
	a.prefs, a.prefsErr = p, nil
	return nil
}

func (a *application) api(w http.ResponseWriter, r *http.Request, op string) {
	if op == "upload" {
		a.upload(w, r)
		return
	}
	var in action
	r.Body = http.MaxBytesReader(w, r.Body, maxTextSize*7)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, errors.New("некорректный запрос"))
		return
	}
	var err error
	switch op {
	case "state":
	case "quit":
		a.disconnect()
		send(w, http.StatusOK, struct{}{})
		if a.stop != nil {
			_ = http.NewResponseController(w).Flush()
			a.stop()
		}
		return
	case "save-profile":
		err = a.saveProfile(in)
	case "delete-profile":
		err = a.deleteProfile(in.Profile)
	case "connect":
		var warning string
		warning, err = a.connect(in)
		var unknown *unknownHost
		if errors.As(err, &unknown) {
			send(w, http.StatusConflict, map[string]string{"fingerprint": unknown.fingerprint, "error": unknown.Error()})
			return
		}
		if err == nil && warning != "" {
			state := a.state()
			state.Warning = warning
			send(w, http.StatusOK, state)
			return
		}
	case "disconnect":
		a.disconnect()
	case "list", "mkdir", "move", "remove", "read-text", "write-text", "download-ticket":
		if a.remote == nil {
			err = errors.New("сначала подключитесь к серверу")
			break
		}
		if op != "list" && a.dir == "" {
			err = errors.New("сначала откройте папку на сервере")
			break
		}
		_ = a.remote.socket.SetDeadline(time.Now().Add(45 * time.Second))
		defer a.remote.socket.SetDeadline(time.Time{})
		var result any
		result, err = a.fileAction(op, in)
		if err == nil && result != nil {
			send(w, http.StatusOK, result)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	send(w, http.StatusOK, a.state())
}

func normalizeFields(fields connectionFields) (connectionFields, error) {
	fields.Host = strings.Trim(strings.TrimSpace(fields.Host), "[]")
	fields.User = strings.TrimSpace(fields.User)
	fields.StartDir = strings.TrimSpace(fields.StartDir)
	fields.KeyPath = strings.TrimSpace(fields.KeyPath)
	port, err := strconv.Atoi(strings.TrimSpace(fields.Port))
	if fields.Host == "" || strings.ContainsAny(fields.Host, "/\\@ \r\n\t\x00") || (strings.Contains(fields.Host, ":") && net.ParseIP(fields.Host) == nil) || fields.User == "" || err != nil || port < 1 || port > 65535 {
		return connectionFields{}, errors.New("укажите адрес сервера, порт от 1 до 65535 и пользователя SSH")
	}
	fields.Port = strconv.Itoa(port)
	return fields, nil
}

func (a *application) savedSecret(name string, fields connectionFields) []byte {
	if i := a.prefs.profileIndex(name); i >= 0 {
		item := a.prefs.Profiles[i]
		if item.credentialScope() == fields.credentialScope() {
			return item.ProtectedSecret
		}
	}
	return nil
}

func (a *application) saveProfile(in action) error {
	fields, err := normalizeFields(in.connectionFields)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 80 || strings.ContainsAny(name, "\r\n\x00") {
		return errors.New("введите название профиля длиной до 80 символов")
	}
	i := a.prefs.profileIndex(in.Profile)
	if j := a.prefs.profileIndex(name); j >= 0 && j != i {
		return errors.New("профиль с таким названием уже существует")
	}
	item := profile{Name: name, connectionFields: fields}
	if in.Remember {
		if in.Secret != "" {
			item.ProtectedSecret, err = protectSecret(in.Secret, fields.credentialScope())
			if err != nil {
				return err
			}
		} else {
			item.ProtectedSecret = a.savedSecret(in.Profile, fields)
			if len(item.ProtectedSecret) == 0 {
				return errors.New("введите пароль или снимите флажок сохранения пароля")
			}
		}
	}
	next := a.prefs
	next.Profiles = slices.Clone(next.Profiles)
	if i >= 0 {
		next.Profiles[i] = item
	} else {
		next.Profiles = append(next.Profiles, item)
	}
	next.LastProfile = name
	return a.save(next)
}

func (a *application) deleteProfile(name string) error {
	i := a.prefs.profileIndex(name)
	if i < 0 {
		return errors.New("профиль не найден")
	}
	next := a.prefs
	next.Profiles = slices.Delete(slices.Clone(next.Profiles), i, i+1)
	if strings.EqualFold(next.LastProfile, name) {
		next.LastProfile = ""
	}
	return a.save(next)
}

func (a *application) connect(in action) (string, error) {
	if a.remote != nil {
		return "", errors.New("закройте текущее подключение")
	}
	fields, err := normalizeFields(in.connectionFields)
	if err != nil {
		return "", err
	}
	address := net.JoinHostPort(strings.ToLower(fields.Host), fields.Port)
	pin := a.prefs.Fingerprints[address]
	if pin == "" {
		pin = in.Fingerprint
	}
	secret := in.Secret
	if secret == "" && in.Remember {
		if saved := a.savedSecret(in.Profile, fields); len(saved) != 0 {
			secret, err = unprotectSecret(saved, fields.credentialScope())
			if err != nil {
				return "", fmt.Errorf("не удалось прочитать сохранённый пароль. Введите его заново: %w", err)
			}
		}
	}
	client, err := connect(connectionOptions{address, fields.User, secret, fields.KeyPath}, pin)
	if err != nil {
		return "", err
	}
	next := a.prefs
	if next.Fingerprints[address] == "" {
		next.Fingerprints = maps.Clone(next.Fingerprints)
		if next.Fingerprints == nil {
			next.Fingerprints = make(map[string]string)
		}
		next.Fingerprints[address] = pin
	}
	next.LastProfile = ""
	if i := next.profileIndex(in.Profile); i >= 0 {
		next.LastProfile = next.Profiles[i].Name
	} else {
		next.connectionFields = fields
	}
	if err := a.save(next); err != nil {
		client.close()
		return "", err
	}
	a.remote = client
	a.address = address
	_ = client.socket.SetDeadline(time.Now().Add(20 * time.Second))
	defer client.socket.SetDeadline(time.Time{})
	if err := a.list(fields.StartDir); err != nil {
		return "Не удалось открыть начальную папку. Введите путь в строке адреса. " + err.Error(), nil
	}
	return "", nil
}

func (a *application) disconnect() {
	if a.remote != nil {
		a.remote.close()
	}
	a.remote, a.address, a.dir, a.files = nil, "", "", nil
	a.download = downloadTicket{}
}

func (a *application) list(dir string) error {
	if a.dir != "" && dir != "" && !path.IsAbs(dir) {
		dir = a.dir + "/" + dir
	}
	actual, files, err := a.remote.list(dir)
	if err != nil {
		return err
	}
	entries := make([]fileEntry, 0, len(files))
	for _, f := range files {
		if !validName(f.Name()) || !utf8.ValidString(f.Name()) {
			continue
		}
		kind := "other"
		switch {
		case f.IsDir():
			kind = "folder"
		case f.Mode().IsRegular():
			kind = "file"
		case f.Mode()&os.ModeSymlink != 0:
			kind = "link"
		}
		entries = append(entries, fileEntry{f.Name(), kind, f.Size(), f.ModTime()})
	}
	a.dir, a.files = actual, entries
	return nil
}

func (a *application) fileAction(op string, in action) (any, error) {
	r := a.remote
	if op == "list" {
		return nil, a.list(strings.TrimSpace(in.Path))
	}
	if !validName(in.Name) {
		return nil, errors.New("некорректное имя файла или папки")
	}
	file := path.Join(a.dir, in.Name)
	var err error
	switch op {
	case "mkdir":
		err = r.Mkdir(file)
	case "move":
		to := strings.TrimSpace(in.Path)
		if to == "" || strings.HasSuffix(to, "/") || !validName(path.Base(to)) || strings.ContainsAny(to, "\x00\r\n") {
			return nil, errors.New("введите путь назначения с именем объекта")
		}
		if !path.IsAbs(to) {
			to = a.dir + "/" + to
		}
		err = r.move(file, to)
	case "remove":
		var info os.FileInfo
		info, err = r.Lstat(file)
		if err == nil && info.IsDir() {
			err = r.RemoveDirectory(file)
		} else if err == nil {
			err = r.Remove(file)
		}
	case "read-text":
		data, err := r.readText(file)
		if err != nil {
			return nil, err
		}
		return map[string]string{"text": string(data), "digest": fmt.Sprintf("%x", sha256.Sum256(data))}, nil
	case "write-text":
		var digest [32]byte
		if len(in.Digest) != hex.EncodedLen(len(digest)) {
			return nil, errors.New("откройте файл в редакторе заново")
		}
		if _, decodeErr := hex.Decode(digest[:], []byte(in.Digest)); decodeErr != nil {
			return nil, errors.New("откройте файл в редакторе заново")
		}
		err = r.saveText(file, digest, []byte(in.Text))
	case "download-ticket":
		info, err := r.Lstat(file)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("скачать можно только обычный файл")
		}
		a.download = downloadTicket{rand.Text(), file, time.Now().Add(time.Minute)}
		return map[string]string{"url": "/download?ticket=" + a.download.key}, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, a.list(a.dir)
}

func (a *application) upload(w http.ResponseWriter, r *http.Request) {
	if a.remote == nil || a.dir == "" {
		fail(w, http.StatusBadRequest, errors.New("откройте папку на сервере"))
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || !validName(part.FileName()) {
		fail(w, http.StatusBadRequest, errors.New("выберите файл для загрузки"))
		return
	}
	target := path.Join(a.dir, part.FileName())
	remote := a.remote
	_ = remote.socket.SetDeadline(time.Now().Add(45 * time.Second))
	defer remote.socket.SetDeadline(time.Time{})
	old, err := remote.existing(target)
	if err == nil && old != nil && r.URL.Query().Get("replace") != "yes" {
		send(w, http.StatusConflict, map[string]string{"error": "файл с таким именем уже существует", "collision": part.FileName()})
		return
	}
	if err == nil {
		err = remote.writeFile(target, transferReader{part, remote.socket}, old)
	}
	if err == nil {
		_ = remote.socket.SetDeadline(time.Now().Add(45 * time.Second))
		err = a.list(a.dir)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	send(w, http.StatusOK, a.state())
}

func (a *application) serveDownload(w http.ResponseWriter, r *http.Request) {
	if !a.mu.TryLock() {
		http.Error(w, "Дождитесь завершения текущей операции", http.StatusConflict)
		return
	}
	defer a.mu.Unlock()
	ticket := a.download
	if a.remote == nil || ticket.key == "" || time.Now().After(ticket.expires) || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("ticket")), []byte(ticket.key)) != 1 {
		http.NotFound(w, r)
		return
	}
	a.download = downloadTicket{}
	_ = a.remote.socket.SetDeadline(time.Now().Add(45 * time.Second))
	defer a.remote.socket.SetDeadline(time.Time{})
	info, err := a.remote.Lstat(ticket.file)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := a.remote.Open(ticket.file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(ticket.file)}))
	_, _ = io.CopyN(w, transferReader{f, a.remote.socket}, info.Size())
}
