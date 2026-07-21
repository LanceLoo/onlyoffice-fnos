package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"onlyoffice-fnos/internal/config"
	"onlyoffice-fnos/internal/file"
	"onlyoffice-fnos/internal/format"
	"onlyoffice-fnos/internal/jwt"
)

// createConvertTestServer creates a test server with a mock OnlyOffice Document Server
func createConvertTestServer(t *testing.T, tempDir string, convertedContent []byte) (*Server, *httptest.Server) {
	return createConvertTestServerWithConvertCallback(t, tempDir, convertedContent, nil)
}

func createConvertTestServerWithConvertCallback(t *testing.T, tempDir string, convertedContent []byte, onConvert func()) (*Server, *httptest.Server) {
	mockDocServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/converter" {
			if onConvert != nil {
				onConvert()
			}
			// Return conversion response with file URL pointing to ourselves
			resp := ConvertResponse{
				EndConvert: true,
				FileURL:    "http://" + r.Host + "/download",
				Percent:    100,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/download" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(convertedContent)
			return
		}
		http.NotFound(w, r)
	}))

	settings := &config.Settings{
		DocumentServerURL: mockDocServer.URL,
	}

	server := New(&Config{
		Settings:      settings,
		FileService:   file.NewService(tempDir, 0),
		FormatManager: format.NewManager(),
		JWTManager:    jwt.NewManager(),
		BaseURL:       "http://localhost:10099",
	})

	return server, mockDocServer
}

// convertTestLogicalPath matches file.Service's path resolution on each host.
// Linux requires an absolute path inside the service basePath because relative
// logical paths are normalized to root-relative paths before containment checks.
func convertTestLogicalPath(tempDir, name string) string {
	if runtime.GOOS == "linux" {
		return filepath.Join(tempDir, name)
	}
	return name
}

func convertTestFilePath(tempDir, logicalPath string) string {
	if filepath.IsAbs(logicalPath) {
		return logicalPath
	}
	return filepath.Join(tempDir, logicalPath)
}

// TestConvertNoConflict tests conversion when target file does not exist
func TestConvertNoConflict(t *testing.T) {
	requireLinuxNoReplace(t)
	tempDir, err := os.MkdirTemp("", "convert_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create source file
	sourceContent := []byte("old xls content")
	sourcePath := filepath.Join(tempDir, "test.xls")
	if err := os.WriteFile(sourcePath, sourceContent, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	convertedContent := []byte("new xlsx content")
	server, mockServer := createConvertTestServer(t, tempDir, convertedContent)
	defer mockServer.Close()

	// Send convert request
	form := url.Values{}
	form.Set("path", convertTestLogicalPath(tempDir, "test.xls"))
	req := httptest.NewRequest("POST", "/convert", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Fatalf("Expected success=true, got %v", resp["success"])
	}

	// Verify target file was created
	targetPath := filepath.Join(tempDir, "test.xlsx")
	savedContent, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("Failed to read target file: %v", err)
	}

	if !bytes.Equal(savedContent, convertedContent) {
		t.Fatalf("Saved content does not match. Expected: %s, Got: %s", convertedContent, savedContent)
	}
}

// TestConvertConflict tests that 409 is returned when target exists
func TestConvertConflict(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "convert_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create source and target files
	sourcePath := filepath.Join(tempDir, "test.xls")
	if err := os.WriteFile(sourcePath, []byte("old"), 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	targetPath := filepath.Join(tempDir, "test.xlsx")
	if err := os.WriteFile(targetPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	convertedContent := []byte("new content")
	server, mockServer := createConvertTestServer(t, tempDir, convertedContent)
	defer mockServer.Close()

	// Send convert request without resolution parameters
	form := url.Values{}
	logicalSourcePath := convertTestLogicalPath(tempDir, "test.xls")
	logicalTargetPath := convertTestLogicalPath(tempDir, "test.xlsx")
	form.Set("path", logicalSourcePath)
	req := httptest.NewRequest("POST", "/convert", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("Expected status 409, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	var resp ConflictResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !resp.Conflict {
		t.Fatalf("Expected conflict=true, got %v", resp.Conflict)
	}

	if resp.TargetPath != logicalTargetPath {
		t.Fatalf("Expected targetPath=%s, got %s", logicalTargetPath, resp.TargetPath)
	}

	// Verify target file was NOT overwritten
	content, _ := os.ReadFile(targetPath)
	if string(content) != "existing" {
		t.Fatalf("Target file was modified. Expected 'existing', got '%s'", string(content))
	}
}

// TestConvertOverwrite tests conversion with overwrite=true
func TestConvertOverwrite(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "convert_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create source and target files
	sourcePath := filepath.Join(tempDir, "test.xls")
	if err := os.WriteFile(sourcePath, []byte("old"), 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	targetPath := filepath.Join(tempDir, "test.xlsx")
	if err := os.WriteFile(targetPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	convertedContent := []byte("new content")
	server, mockServer := createConvertTestServer(t, tempDir, convertedContent)
	defer mockServer.Close()

	// Send convert request with overwrite=true
	form := url.Values{}
	form.Set("path", convertTestLogicalPath(tempDir, "test.xls"))
	req := httptest.NewRequest("POST", "/convert?overwrite=true", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	// Verify target file was overwritten
	content, _ := os.ReadFile(targetPath)
	if string(content) != string(convertedContent) {
		t.Fatalf("Target file was not overwritten. Expected '%s', got '%s'", convertedContent, string(content))
	}
}

// TestConvertAutoRename tests conversion with auto_rename=true
func TestConvertAutoRename(t *testing.T) {
	requireLinuxNoReplace(t)
	tempDir, err := os.MkdirTemp("", "convert_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create source and target files
	sourcePath := filepath.Join(tempDir, "test.xls")
	if err := os.WriteFile(sourcePath, []byte("old"), 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	targetPath := filepath.Join(tempDir, "test.xlsx")
	if err := os.WriteFile(targetPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	convertedContent := []byte("new content")
	server, mockServer := createConvertTestServer(t, tempDir, convertedContent)
	defer mockServer.Close()

	// Send convert request with auto_rename=true
	form := url.Values{}
	form.Set("path", convertTestLogicalPath(tempDir, "test.xls"))
	req := httptest.NewRequest("POST", "/convert?auto_rename=true", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	// Verify new file was created with (converted) suffix
	newPath := filepath.Join(tempDir, "test (converted).xlsx")
	content, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("Failed to read auto-renamed file: %v", err)
	}

	if string(content) != string(convertedContent) {
		t.Fatalf("Auto-renamed file content mismatch. Expected '%s', got '%s'", convertedContent, string(content))
	}

	// Verify original target file was NOT overwritten
	originalContent, _ := os.ReadFile(targetPath)
	if string(originalContent) != "existing" {
		t.Fatalf("Original target file was modified. Expected 'existing', got '%s'", string(originalContent))
	}
}

// TestConvertAutoRenameMultiple tests multiple auto-rename operations
func TestConvertAutoRenameMultiple(t *testing.T) {
	requireLinuxNoReplace(t)
	tempDir, err := os.MkdirTemp("", "convert_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create source file
	sourcePath := filepath.Join(tempDir, "test.xls")
	if err := os.WriteFile(sourcePath, []byte("old"), 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Create conflicting target files
	targetPath := filepath.Join(tempDir, "test.xlsx")
	if err := os.WriteFile(targetPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	convertedPath := filepath.Join(tempDir, "test (converted).xlsx")
	if err := os.WriteFile(convertedPath, []byte("existing2"), 0644); err != nil {
		t.Fatalf("Failed to create converted file: %v", err)
	}

	convertedContent := []byte("new content")
	server, mockServer := createConvertTestServer(t, tempDir, convertedContent)
	defer mockServer.Close()

	// Send convert request with auto_rename=true
	form := url.Values{}
	form.Set("path", convertTestLogicalPath(tempDir, "test.xls"))
	req := httptest.NewRequest("POST", "/convert?auto_rename=true", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	// Verify new file was created with (converted 2) suffix
	newPath := filepath.Join(tempDir, "test (converted 2).xlsx")
	content, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("Failed to read auto-renamed file: %v", err)
	}

	if string(content) != string(convertedContent) {
		t.Fatalf("Auto-renamed file content mismatch. Expected '%s', got '%s'", convertedContent, string(content))
	}
}

func TestConvertAutoRenameExhaustedNamesReturnsConflict(t *testing.T) {
	requireLinuxNoReplace(t)
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "test.xls"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	server, mockServer := createConvertTestServer(t, tempDir, []byte("converted"))
	defer mockServer.Close()
	logicalTargetPath := convertTestLogicalPath(tempDir, "test.xlsx")
	for _, candidate := range server.autoRenameCandidates(logicalTargetPath) {
		if err := os.WriteFile(convertTestFilePath(tempDir, candidate), []byte("existing"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/convert?path="+url.QueryEscape(convertTestLogicalPath(tempDir, "test.xls"))+"&auto_rename=true", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var response ConflictResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Message != "No available converted file name; please rename or remove an existing converted file" {
		t.Fatalf("unexpected exhausted-name message: %q", response.Message)
	}
	if response.TargetPath != logicalTargetPath {
		t.Fatalf("expected original target path %q, got %q", logicalTargetPath, response.TargetPath)
	}
}

func TestConvertRejectsOverwriteWithAutoRename(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "test.xls"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	server, mockServer := createConvertTestServer(t, tempDir, []byte("new"))
	defer mockServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/convert?overwrite=true&auto_rename=true", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAutoRenameCandidatesIncludeVariantsThroughTen(t *testing.T) {
	server := &Server{}
	candidates := server.autoRenameCandidates("test.xlsx")
	if len(candidates) != maxAutoRenameVariants+1 {
		t.Fatalf("expected base path plus %d variants, got %d candidates", maxAutoRenameVariants, len(candidates))
	}
	if candidates[0] != "test.xlsx" || candidates[10] != "test (converted 10).xlsx" {
		t.Fatalf("unexpected candidate sequence endpoints: %q, %q", candidates[0], candidates[10])
	}
}

func TestConvertDefaultPublishRaceReturnsConflict(t *testing.T) {
	requireLinuxNoReplace(t)
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "test.xls"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(tempDir, "test.xlsx")
	injected := []byte("created during conversion")
	server, mockServer := createConvertTestServerWithConvertCallback(t, tempDir, []byte("converted"), func() {
		if err := os.WriteFile(targetPath, injected, 0644); err != nil {
			t.Errorf("inject target: %v", err)
		}
	})
	defer mockServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/convert?path="+url.QueryEscape(convertTestLogicalPath(tempDir, "test.xls")), nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}
	content, err := os.ReadFile(targetPath)
	if err != nil || !bytes.Equal(content, injected) {
		t.Fatalf("injected target was not preserved: %q, %v", content, err)
	}
}

func TestConvertAutoRenamePublishRaceUsesLaterCandidate(t *testing.T) {
	requireLinuxNoReplace(t)
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "test.xls"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "test.xlsx"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	injectedPath := filepath.Join(tempDir, "test (converted).xlsx")
	injected := []byte("created during conversion")
	converted := []byte("converted")
	server, mockServer := createConvertTestServerWithConvertCallback(t, tempDir, converted, func() {
		if err := os.WriteFile(injectedPath, injected, 0644); err != nil {
			t.Errorf("inject renamed target: %v", err)
		}
	})
	defer mockServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/convert?path="+url.QueryEscape(convertTestLogicalPath(tempDir, "test.xls"))+"&auto_rename=true", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		TargetPath string `json:"targetPath"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	expectedTargetPath := convertTestLogicalPath(tempDir, "test (converted 2).xlsx")
	if response.TargetPath != expectedTargetPath {
		t.Fatalf("expected later candidate, got %q", response.TargetPath)
	}
	content, err := os.ReadFile(injectedPath)
	if err != nil || !bytes.Equal(content, injected) {
		t.Fatalf("injected candidate was not preserved: %q, %v", content, err)
	}
	content, err = os.ReadFile(convertTestFilePath(tempDir, response.TargetPath))
	if err != nil || !bytes.Equal(content, converted) {
		t.Fatalf("later candidate was not saved: %q, %v", content, err)
	}
}

func requireLinuxNoReplace(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("atomic no-replace publication is supported only on Linux")
	}
}
