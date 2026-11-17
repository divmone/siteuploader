package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalRequestAuthorization(t *testing.T) {
	const host = "127.0.0.1:46670"
	const token = "test-session-token"
	a := &application{host: host, token: token, prefs: preferences{Profiles: []profile{{Name: "Test profile", ProtectedSecret: []byte("protected-test-secret")}}}}
	static := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := a.handler(static)

	for _, tc := range []struct {
		name, host, origin, key, method string
		status                          int
	}{
		{"local", host, "http://" + host, token, http.MethodPost, http.StatusOK},
		{"unauthenticated", host, "", "", http.MethodPost, http.StatusForbidden},
		{"wrong origin", host, "https://example.org", token, http.MethodPost, http.StatusForbidden},
		{"wrong host", "example.org", "", token, http.MethodPost, http.StatusForbidden},
		{"wrong method", host, "", token, http.MethodGet, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/state", strings.NewReader("{}"))
			req.Header.Set("X-Site-Uploader", tc.key)
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("got %d; want %d", rec.Code, tc.status)
			}
			body := rec.Body.String()
			if strings.Contains(body, "protected_secret") || (tc.status != http.StatusOK && strings.Contains(body, "Test profile")) {
				t.Fatal("response disclosed private settings")
			}
		})
	}
}
