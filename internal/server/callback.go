package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	jwtpkg "onlyoffice-fnos/internal/jwt"
)

const callbackSessionTTL = 7 * 24 * time.Hour
const callbackMaxBodyBytes = 1 << 20

type callbackSession struct {
	Key      string `json:"key"`
	Path     string `json:"path"`
	FileType string `json:"fileType"`
	Expires  int64  `json:"expires"`
}

type callbackPathLock struct {
	mu   sync.Mutex
	refs int
}

// CallbackStatus represents the document status from OnlyOffice.
type CallbackStatus int

const (
	StatusEditing        CallbackStatus = 1
	StatusSaved          CallbackStatus = 2
	StatusSaveError      CallbackStatus = 3
	StatusClosed         CallbackStatus = 4
	StatusForceSave      CallbackStatus = 6
	StatusForceSaveError CallbackStatus = 7
)

type CallbackAction struct {
	Type   int    `json:"type"`
	UserID string `json:"userid"`
}

type CallbackRequest struct {
	Actions    []CallbackAction `json:"actions,omitempty"`
	Key        string           `json:"key"`
	Status     CallbackStatus   `json:"status"`
	Users      []string         `json:"users,omitempty"`
	URL        string           `json:"url,omitempty"`
	Token      string           `json:"token,omitempty"`
	Changesurl string           `json:"changesurl,omitempty"`
	History    json.RawMessage  `json:"history,omitempty"`
	Filetype   string           `json:"filetype,omitempty"`
}

type CallbackResponse struct {
	Error int `json:"error"`
}

func normalizeFileType(fileType string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(fileType), "."))
}

func (s *Server) issueCallbackSession(key, path, fileType string) (string, error) {
	fileType = normalizeFileType(fileType)
	if key == "" || path == "" || fileType == "" {
		return "", fmt.Errorf("callback session requires key, path, and file type")
	}
	payload, err := json.Marshal(callbackSession{Key: key, Path: path, FileType: fileType, Expires: s.now().Add(callbackSessionTTL).Unix()})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.callbackSecret)
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifyCallbackSession(token string) (*callbackSession, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid callback session format")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid callback session signature")
	}
	mac := hmac.New(sha256.New, s.callbackSecret)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid callback session signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid callback session payload")
	}
	var session callbackSession
	if err := json.Unmarshal(payload, &session); err != nil {
		return nil, fmt.Errorf("invalid callback session payload")
	}
	session.FileType = normalizeFileType(session.FileType)
	if session.Key == "" || session.Path == "" || session.FileType == "" || session.Expires <= s.now().Unix() {
		return nil, fmt.Errorf("expired or incomplete callback session")
	}
	return &session, nil
}

func (s *Server) lockCallbackPath(path string) func() {
	s.callbackLocksMu.Lock()
	entry := s.callbackLocks[path]
	if entry == nil {
		entry = &callbackPathLock{}
		s.callbackLocks[path] = entry
	}
	entry.refs++
	s.callbackLocksMu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.callbackLocksMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.callbackLocks, path)
		}
		s.callbackLocksMu.Unlock()
	}
}

func callbackRequestFromClaims(claims map[string]interface{}) (CallbackRequest, error) {
	key, keyOK := claims["key"].(string)
	statusValue, statusOK := claims["status"].(float64)
	if !keyOK || key == "" || !statusOK || statusValue != float64(int(statusValue)) {
		return CallbackRequest{}, fmt.Errorf("invalid callback JWT claims")
	}
	req := CallbackRequest{Key: key, Status: CallbackStatus(statusValue)}
	if url, ok := claims["url"].(string); ok {
		req.URL = url
	}
	if fileType, ok := claims["filetype"].(string); ok {
		req.Filetype = fileType
	}
	if req.Status == StatusSaved || req.Status == StatusForceSave {
		if req.URL == "" || normalizeFileType(req.Filetype) == "" {
			return CallbackRequest{}, fmt.Errorf("incomplete save callback JWT claims")
		}
	}
	return req, nil
}

func (s *Server) callbackError(w http.ResponseWriter, message string) {
	log.Printf("Callback error: %s", message)
	s.respondJSON(w, http.StatusOK, &CallbackResponse{Error: 1})
}

// handleCallback handles a capability-authorized Document Server callback.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	var req CallbackRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, callbackMaxBodyBytes))
	if err := decoder.Decode(&req); err != nil {
		s.callbackError(w, "failed to parse request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		s.callbackError(w, "callback request contains trailing data")
		return
	}
	session, err := s.verifyCallbackSession(r.URL.Query().Get("session"))
	if err != nil {
		s.callbackError(w, err.Error())
		return
	}
	if s.settings != nil && s.settings.DocumentServerSecret != "" {
		if req.Token == "" {
			s.callbackError(w, "missing JWT token")
			return
		}
		claims, err := s.jwtManager.Verify(s.settings.DocumentServerSecret, req.Token)
		if err != nil {
			if err == jwtpkg.ErrExpiredToken {
				log.Printf("Callback error: token has expired")
			}
			s.callbackError(w, "invalid JWT token")
			return
		}
		req, err = callbackRequestFromClaims(claims)
		if err != nil {
			s.callbackError(w, err.Error())
			return
		}
	}
	if req.Key != session.Key {
		s.callbackError(w, "callback key does not match session")
		return
	}
	if req.Status == StatusSaved || req.Status == StatusForceSave {
		if req.URL == "" || normalizeFileType(req.Filetype) != session.FileType {
			s.callbackError(w, "missing URL or file type does not match session")
			return
		}
		var saveErr error
		func() {
			unlock := s.lockCallbackPath(session.Path)
			defer unlock()
			saveErr = s.saveDocument(r.Context(), session.Path, req.URL)
		}()
		err := saveErr
		if err != nil {
			s.callbackError(w, fmt.Sprintf("failed to save document: %v", err))
			return
		}
	}
	s.respondJSON(w, http.StatusOK, &CallbackResponse{Error: 0})
}

func (s *Server) saveDocument(ctx context.Context, filePath, documentURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, documentURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create document download request: %w", err)
	}
	resp, err := s.documentClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download document: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("document server returned status %d", resp.StatusCode)
	}
	if err := s.fileService.SaveFile(filePath, &contextReader{ctx: ctx, reader: resp.Body}); err != nil {
		return fmt.Errorf("failed to save document: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("document download context ended after save: %w", err)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *Server) SaveDocumentFromReader(filePath string, content io.Reader) error {
	return s.fileService.SaveFile(filePath, content)
}
