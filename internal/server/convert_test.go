package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"onlyoffice-fnos/internal/config"
	"onlyoffice-fnos/internal/file"
	"onlyoffice-fnos/internal/format"
	"onlyoffice-fnos/internal/jwt"
)

// createConvertTestServer creates a test server with a mock OnlyOffice Document Server
func createConvertTestServer(t *testing.T, tempDir string, convertedContent []byte) (*Server, *httptest.Server) {
	mockDocServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/ConvertService.ashx" {
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

// TestConvertNoConflict tests conversion when target file does not exist
func TestConvertNoConflict(t *testing.T) {
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
	form.Set("path", "test.xls")
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
	form.Set("path", "test.xls")
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

	if resp.TargetPath != "test.xlsx" {
		t.Fatalf("Expected targetPath=/test.xlsx, got %s", resp.TargetPath)
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
	form.Set("path", "test.xls")
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
	form.Set("path", "test.xls")
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
	form.Set("path", "test.xls")
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

// TestGenerateUniqueTargetPath tests the unique path generation logic directly
func TestGenerateUniqueTargetPath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "convert_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	server, mockServer := createConvertTestServer(t, tempDir, []byte(""))
	defer mockServer.Close()

	basePath := "test.xlsx"

	// Test 1: no conflict
	path, err := server.generateUniqueTargetPath(basePath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if path != basePath {
		t.Fatalf("Expected %s, got %s", basePath, path)
	}

	// Test 2: base path exists, should get (converted)
	os.WriteFile(filepath.Join(tempDir, "test.xlsx"), []byte(""), 0644)
	path, err = server.generateUniqueTargetPath(basePath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected := "test (converted).xlsx"
	if path != expected {
		t.Fatalf("Expected %s, got %s", expected, path)
	}

	// Test 3: (converted) exists, should get (converted 2)
	os.WriteFile(filepath.Join(tempDir, "test (converted).xlsx"), []byte(""), 0644)
	path, err = server.generateUniqueTargetPath(basePath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected = "test (converted 2).xlsx"
	if path != expected {
		t.Fatalf("Expected %s, got %s", expected, path)
	}
}
