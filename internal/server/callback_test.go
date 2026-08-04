package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"onlyoffice-fnos/internal/config"
	"onlyoffice-fnos/internal/file"
	"onlyoffice-fnos/internal/format"
	"onlyoffice-fnos/internal/jwt"
)

func TestCallbackRouteUsesCallbackTimeout(t *testing.T) {
	directory := t.TempDir()
	s := createTestServer(t, directory, "")
	deadline := make(chan time.Time, 1)
	s.documentClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		value, ok := r.Context().Deadline()
		if !ok {
			t.Error("document download context has no deadline")
			return nil, context.DeadlineExceeded
		}
		deadline <- value
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("saved"))), Header: make(http.Header)}, nil
	})}

	path := filepath.Join(directory, "document.pdf")
	started := time.Now()
	response := postCallback(t, s, callbackURL(t, s, "key", path, "pdf"), CallbackRequest{
		Key: "key", Status: StatusSaved, URL: "http://document-server.test/document.pdf", Filetype: "pdf",
	})
	if response.Error != 0 {
		t.Fatalf("callback returned %d", response.Error)
	}
	got := <-deadline
	remaining := time.Until(got)
	if remaining < CallbackTimeout-2*time.Second || remaining > CallbackTimeout+time.Second {
		t.Fatalf("callback deadline remaining = %v, want approximately %v (request started %v ago)", remaining, CallbackTimeout, time.Since(started))
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSaveDocumentCancelledContextDoesNotWrite(t *testing.T) {
	directory := t.TempDir()
	s := createTestServer(t, directory, "")
	path := filepath.Join(directory, "document.pdf")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	documentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-r.Context().Done()
	}))
	defer documentServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.saveDocument(ctx, path, documentServer.URL); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, []byte("original")) {
		t.Fatalf("cancelled download changed target: %q, err = %v", got, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("cancelled download reached document server %d times", requests.Load())
	}
}

func createTestServer(t *testing.T, directory, secret string) *Server {
	t.Helper()
	return New(&Config{Settings: &config.Settings{DocumentServerURL: "http://example.com", DocumentServerSecret: secret}, FileService: file.NewService(directory, 0), FormatManager: format.NewManager(), JWTManager: jwt.NewManager(), BaseURL: "http://localhost:10099"})
}

func callbackURL(t *testing.T, s *Server, key, path, fileType string) string {
	t.Helper()
	session, err := s.issueCallbackSession(key, path, fileType)
	if err != nil {
		t.Fatal(err)
	}
	return "/callback?session=" + session
}

func postCallback(t *testing.T, s *Server, target string, callback CallbackRequest) CallbackResponse {
	t.Helper()
	body, err := json.Marshal(callback)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body)))
	var response CallbackResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func postCallbackBody(t *testing.T, s *Server, target string, body []byte) CallbackResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body)))
	var response CallbackResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func assertCallbackLocksClean(t *testing.T, s *Server) {
	t.Helper()
	s.callbackLocksMu.Lock()
	defer s.callbackLocksMu.Unlock()
	if len(s.callbackLocks) != 0 {
		t.Fatalf("callback lock table not cleaned: %#v", s.callbackLocks)
	}
}

func TestCallbackPDFSaveSessions(t *testing.T) {
	directory := t.TempDir()
	s := createTestServer(t, directory, "")
	content := []byte("saved PDF")
	documentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(content) }))
	defer documentServer.Close()

	for _, status := range []CallbackStatus{StatusSaved, StatusForceSave} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			path := filepath.Join(directory, "document-"+string(rune('0'+status))+".pdf")
			response := postCallback(t, s, callbackURL(t, s, "pdf-key-"+string(rune('0'+status)), path, "pdf"), CallbackRequest{Key: "pdf-key-" + string(rune('0'+status)), Status: status, URL: documentServer.URL, Filetype: ".PDF"})
			if response.Error != 0 {
				t.Fatalf("callback returned %d", response.Error)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("saved content = %q, err = %v", got, err)
			}
		})
	}
}

func TestCallbackSessionValidation(t *testing.T) {
	directory := t.TempDir()
	s := createTestServer(t, directory, "")
	path := filepath.Join(directory, "document.pdf")
	validURL := callbackURL(t, s, "key", path, "pdf")
	valid := CallbackRequest{Key: "key", Status: StatusSaved, URL: "http://example.com", Filetype: "pdf"}

	tests := []struct {
		name, target string
		request      CallbackRequest
	}{
		{"missing session", "/callback", valid},
		{"tampered session", validURL + "x", valid},
		{"key mismatch", validURL, CallbackRequest{Key: "other", Status: StatusSaved, URL: "http://example.com", Filetype: "pdf"}},
		{"missing PDF file type", validURL, CallbackRequest{Key: "key", Status: StatusSaved, URL: "http://example.com"}},
		{"wrong PDF file type", validURL, CallbackRequest{Key: "key", Status: StatusSaved, URL: "http://example.com", Filetype: "docx"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if response := postCallback(t, s, test.target, test.request); response.Error != 1 {
				t.Fatalf("callback returned %d, want 1", response.Error)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid callback wrote a file: %v", err)
			}
		})
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	expiredURL := callbackURL(t, s, "key", path, "pdf")
	if got := callbackSessionTTL; got != 24*time.Hour {
		t.Fatalf("callback session TTL = %v, want 24h", got)
	}
	now = now.Add(callbackSessionTTL + time.Second)
	if response := postCallback(t, s, expiredURL, valid); response.Error != 1 {
		t.Fatalf("expired callback returned %d", response.Error)
	}
}

func TestCallbackRejectsUnknownStatus(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "document.docx")

	t.Run("without JWT", func(t *testing.T) {
		s := createTestServer(t, directory, "")
		response := postCallback(t, s, callbackURL(t, s, "key", path, "docx"), CallbackRequest{Key: "key", Status: CallbackStatus(5)})
		if response.Error != 1 {
			t.Fatalf("unknown callback status returned %d, want 1", response.Error)
		}
	})

	t.Run("from JWT", func(t *testing.T) {
		const secret = "test-secret"
		s := createTestServer(t, directory, secret)
		token, err := s.jwtManager.Sign(secret, map[string]interface{}{"key": "key", "status": 5})
		if err != nil {
			t.Fatal(err)
		}
		response := postCallback(t, s, callbackURL(t, s, "key", path, "docx"), CallbackRequest{Key: "key", Status: StatusEditing, Token: token})
		if response.Error != 1 {
			t.Fatalf("unknown JWT callback status returned %d, want 1", response.Error)
		}
	})
}

func TestAccessLoggerRedactsCallbackSession(t *testing.T) {
	directory := t.TempDir()
	s := createTestServer(t, directory, "")
	callbackTarget := callbackURL(t, s, "key", filepath.Join(directory, "document.docx"), "docx")
	callbackParts := strings.SplitN(callbackTarget, "?", 2)
	if len(callbackParts) != 2 {
		t.Fatalf("callback target has no query: %q", callbackTarget)
	}
	session := strings.TrimPrefix(callbackParts[1], "session=")

	var logs bytes.Buffer
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})

	if response := postCallback(t, s, callbackTarget, CallbackRequest{Key: "key", Status: StatusEditing}); response.Error != 0 {
		t.Fatalf("callback returned %d", response.Error)
	}
	for _, target := range []string{
		"/callback/?session=" + session,
		"/callback/extra?visible=value&session=" + session,
		"/missing?session=" + session,
	} {
		s.Router().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	}
	s.Router().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/static/missing.css?visible=value", nil))

	output := logs.String()
	if strings.Contains(output, "session=") || strings.Contains(output, session) || strings.Contains(output, callbackTarget) {
		t.Fatalf("callback session appeared in access log: %q", output)
	}
	if !strings.Contains(output, "/static/missing.css?visible=value") {
		t.Fatalf("non-callback request was not observable in access log: %q", output)
	}
}

func TestCallbackJWTAndRetry(t *testing.T) {
	directory := t.TempDir()
	secret := "test-secret"
	s := createTestServer(t, directory, secret)
	path := filepath.Join(directory, "document.docx")
	url := callbackURL(t, s, "key", path, "docx")
	request := CallbackRequest{Key: "key", Status: StatusSaved, URL: "http://127.0.0.1:1", Filetype: "docx"}
	if response := postCallback(t, s, url, request); response.Error != 1 {
		t.Fatal("missing JWT should fail")
	}
	token, err := s.jwtManager.Sign(secret, map[string]interface{}{"key": "key", "status": int(StatusSaved), "url": request.URL, "filetype": "docx"})
	if err != nil {
		t.Fatal(err)
	}
	request.Token = token
	if response := postCallback(t, s, url, request); response.Error != 1 {
		t.Fatal("failed save should fail")
	}
	assertCallbackLocksClean(t, s)
	documentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("retry")) }))
	defer documentServer.Close()
	request.URL = documentServer.URL
	request.Token, err = s.jwtManager.Sign(secret, map[string]interface{}{"key": "key", "status": int(StatusSaved), "url": documentServer.URL, "filetype": "docx"})
	if err != nil {
		t.Fatal(err)
	}
	if response := postCallback(t, s, url+"&path="+filepath.Join(directory, "attacker.docx"), request); response.Error != 0 {
		t.Fatal("retry with same session should succeed")
	}
	if _, err := os.Stat(filepath.Join(directory, "attacker.docx")); !os.IsNotExist(err) {
		t.Fatal("path query changed save target")
	}
	assertCallbackLocksClean(t, s)
}

func TestCallbackPathLockSerializesDifferentKeys(t *testing.T) {
	directory := t.TempDir()
	s := createTestServer(t, directory, "")
	relativePath := "same.docx"
	absolutePath := filepath.Join(directory, "same.docx")
	started := make(chan struct{})
	secondCallbackStarted := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSaves := func() { releaseOnce.Do(func() { close(release) }) }
	var first sync.Once
	documentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCall := false
		first.Do(func() { firstCall = true })
		if firstCall {
			close(started)
			<-release
		} else {
			secondStarted <- struct{}{}
		}
		_, _ = w.Write([]byte("saved"))
	}))
	defer documentServer.Close()
	defer releaseSaves()

	response := make(chan CallbackResponse, 2)
	callbackTargets := make(map[string]string, 2)
	for key, path := range map[string]string{"old-key": relativePath, "new-key": absolutePath} {
		target, err := s.buildCallbackURL(key, path, "docx")
		if err != nil {
			t.Fatal(err)
		}
		callbackTargets[key] = target
	}
	go func() {
		response <- postCallbackNoFail(s, callbackTargets["old-key"], CallbackRequest{Key: "old-key", Status: StatusSaved, URL: documentServer.URL, Filetype: "docx"})
	}()
	<-started
	go func() {
		close(secondCallbackStarted)
		response <- postCallbackNoFail(s, callbackTargets["new-key"], CallbackRequest{Key: "new-key", Status: StatusSaved, URL: documentServer.URL, Filetype: "docx"})
	}()
	<-secondCallbackStarted
	deadline := time.After(time.Second)
	for {
		s.callbackLocksMu.Lock()
		entry := s.callbackLocks[absolutePath]
		waiting := len(s.callbackLocks) == 1 && entry != nil && entry.refs == 2
		s.callbackLocksMu.Unlock()
		if waiting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second callback did not wait for the same path lock")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-secondStarted:
		t.Fatal("same path saves ran concurrently")
	default:
	}
	releaseSaves()
	for range 2 {
		if result := <-response; result.Error != 0 {
			t.Fatalf("callback returned %d", result.Error)
		}
	}
	assertCallbackLocksClean(t, s)
}

func TestCallbackJWTClaimsAreAuthoritative(t *testing.T) {
	directory := t.TempDir()
	secret := "test-secret"
	s := createTestServer(t, directory, secret)
	trustedPath := filepath.Join(directory, "trusted.docx")
	documentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("trusted")) }))
	defer documentServer.Close()
	token, err := s.jwtManager.Sign(secret, map[string]interface{}{
		"key": "trusted-key", "status": int(StatusSaved), "url": documentServer.URL, "filetype": "docx",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := postCallback(t, s, callbackURL(t, s, "trusted-key", trustedPath, "docx"), CallbackRequest{
		Key: "attacker-key", Status: StatusEditing, URL: "http://127.0.0.1:1", Filetype: "pdf", Token: token,
	})
	if response.Error != 0 {
		t.Fatalf("trusted callback token returned %d", response.Error)
	}
	if got, err := os.ReadFile(trustedPath); err != nil || string(got) != "trusted" {
		t.Fatalf("trusted token claims did not control save: %q, %v", got, err)
	}
	editorToken, err := s.jwtManager.Sign(secret, map[string]interface{}{"document": map[string]interface{}{"key": "trusted-key"}, "editorConfig": map[string]interface{}{}})
	if err != nil {
		t.Fatal(err)
	}
	if response := postCallback(t, s, callbackURL(t, s, "trusted-key", trustedPath, "docx"), CallbackRequest{Key: "trusted-key", Status: StatusSaved, URL: documentServer.URL, Filetype: "docx", Token: editorToken}); response.Error != 1 {
		t.Fatal("editor configuration JWT must not be accepted as a callback JWT")
	}
}

func postCallbackNoFail(s *Server, target string, callback CallbackRequest) CallbackResponse {
	body, _ := json.Marshal(callback)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body)))
	var response CallbackResponse
	_ = json.NewDecoder(rec.Body).Decode(&response)
	return response
}

func TestCallbackBodyAndNonSaveStatuses(t *testing.T) {
	directory := t.TempDir()
	secret := "test-secret"
	s := createTestServer(t, directory, secret)
	path := filepath.Join(directory, "document.docx")
	for _, status := range []CallbackStatus{StatusEditing, StatusSaveError, StatusClosed, StatusForceSaveError} {
		url := callbackURL(t, s, "key", path, "docx")
		token, err := s.jwtManager.Sign(secret, map[string]interface{}{"key": "key", "status": int(status)})
		if err != nil {
			t.Fatal(err)
		}
		if response := postCallback(t, s, url, CallbackRequest{Key: "key", Status: status, Token: token}); response.Error != 0 {
			t.Errorf("status %d returned %d", status, response.Error)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("non-save status wrote a file")
	}
	url := callbackURL(t, s, "key", path, "docx")
	token, err := s.jwtManager.Sign(secret, map[string]interface{}{"key": "key", "status": int(StatusEditing)})
	if err != nil {
		t.Fatal(err)
	}
	if response := postCallback(t, s, url, CallbackRequest{Key: "key", Status: StatusEditing, Token: "invalid"}); response.Error != 1 {
		t.Fatal("invalid JWT should fail")
	}
	valid, _ := json.Marshal(CallbackRequest{Key: "key", Status: StatusEditing, Token: token})
	if response := postCallbackBody(t, s, url, append(valid, valid...)); response.Error != 1 {
		t.Fatal("second JSON value should fail")
	}
	if response := postCallbackBody(t, s, url, append(valid, []byte(" \n\t")...)); response.Error != 0 {
		t.Fatal("trailing whitespace should be accepted")
	}
	oversized := bytes.Repeat([]byte("x"), callbackMaxBodyBytes+1)
	if response := postCallbackBody(t, s, url, oversized); response.Error != 1 {
		t.Fatal("oversized body should fail")
	}
}

func TestCallbackRejectsExternalPathAndPreservesBinary(t *testing.T) {
	directory := t.TempDir()
	s := createTestServer(t, directory, "")
	binary := []byte{0, 1, 2, 255, 0, 42}
	documentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(binary) }))
	defer documentServer.Close()
	outside := filepath.Join(filepath.Dir(directory), "outside.docx")
	if response := postCallback(t, s, callbackURL(t, s, "outside", outside, "docx"), CallbackRequest{Key: "outside", Status: StatusSaved, URL: documentServer.URL, Filetype: "docx"}); response.Error != 1 {
		t.Fatal("external signed path should still be rejected")
	}
	path := filepath.Join(directory, "binary.docx")
	if response := postCallback(t, s, callbackURL(t, s, "binary", path, "docx"), CallbackRequest{Key: "binary", Status: StatusSaved, URL: documentServer.URL, Filetype: "docx"}); response.Error != 0 {
		t.Fatal("binary save failed")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("binary content was not preserved: %v", err)
	}
	assertCallbackLocksClean(t, s)
}
