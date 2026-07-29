package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onlyoffice-fnos/internal/config"
	"onlyoffice-fnos/internal/file"
	"onlyoffice-fnos/internal/format"
	"onlyoffice-fnos/internal/jwt"
)

func createPageTestServer(t *testing.T, tempDir string) *Server {
	t.Helper()
	return New(&Config{
		Settings:      &config.Settings{DocumentServerURL: "http://localhost:9980"},
		FileService:   file.NewService(tempDir, 0),
		FormatManager: format.NewManager(),
		JWTManager:    jwt.NewManager(),
		BaseURL:       "http://localhost:10099",
	})
}

func getConvertPage(t *testing.T, server *Server, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/convert?path="+path, nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func assertContainsAll(t *testing.T, body string, snippets ...string) {
	t.Helper()
	for _, snippet := range snippets {
		if !strings.Contains(body, snippet) {
			t.Fatalf("page is missing %q", snippet)
		}
	}
}

// csvRetryMergeSnippet is the exact JS expression that forwards the CSV
// encoding/delimiter selection on overwrite, auto-rename, and
// compatibility-mode retry requests.
const csvRetryMergeSnippet = `Object.assign({ path: currentSourcePath }, csvConversionParams())`

// assertCSVRetryPaths asserts that all three retry paths (overwrite,
// auto-rename, compatibility mode) forward the CSV selection.
func assertCSVRetryPaths(t *testing.T, body string) {
	t.Helper()
	if count := strings.Count(body, csvRetryMergeSnippet); count != 3 {
		t.Fatalf("expected %q on all 3 retry paths, found %d occurrences", csvRetryMergeSnippet, count)
	}
}

func TestConvertPageShowsCSVOptionsWithDefaults(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "test.csv"), []byte("a,b\n1,2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	server := createPageTestServer(t, tempDir)

	body := getConvertPage(t, server, "test.csv")
	assertContainsAll(t, body,
		`name="codePage"`,
		`<option value="936" selected>GBK / CP936</option>`,
		`<option value="65001">UTF-8</option>`,
		`name="delimiter"`,
		`<option value="4" selected>逗号 (,)</option>`,
		`<option value="1">Tab</option>`,
		`<option value="2">分号 (;)</option>`,
		// Retry paths must forward the selected CSV parameters.
		`csvConversionParams()`,
	)
	assertCSVRetryPaths(t, body)
}

func TestConvertPageHidesCSVOptionsForNonCSV(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "test.xls"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	server := createPageTestServer(t, tempDir)

	body := getConvertPage(t, server, "test.xls")
	for _, snippet := range []string{`name="codePage"`, `name="delimiter"`} {
		if strings.Contains(body, snippet) {
			t.Fatalf("non-CSV page unexpectedly contains %q", snippet)
		}
	}
	// Retry helper stays harmless without the selectors.
	assertContainsAll(t, body, `csvConversionParams()`)
}

func TestConvertPageFallbackCSVOptions(t *testing.T) {
	server := &Server{}

	rec := httptest.NewRecorder()
	server.renderConvertPageFallback(rec, &ConvertPageData{
		FileName:        "test.csv",
		FilePath:        "test.csv",
		FilePathEncoded: "test.csv",
		SourceFormat:    "csv",
		TargetFormat:    "xlsx",
		IsCSV:           true,
	})
	body := rec.Body.String()
	assertContainsAll(t, body,
		`name="codePage"`,
		`<option value="936" selected>GBK / CP936</option>`,
		`<option value="65001">UTF-8</option>`,
		`name="delimiter"`,
		`<option value="4" selected>逗号 (,)</option>`,
	)
	assertCSVRetryPaths(t, body)

	rec = httptest.NewRecorder()
	server.renderConvertPageFallback(rec, &ConvertPageData{
		FileName:        "test.xls",
		FilePath:        "test.xls",
		FilePathEncoded: "test.xls",
		SourceFormat:    "xls",
		TargetFormat:    "xlsx",
	})
	body = rec.Body.String()
	for _, snippet := range []string{`name="codePage"`, `name="delimiter"`} {
		if strings.Contains(body, snippet) {
			t.Fatalf("non-CSV fallback page unexpectedly contains %q", snippet)
		}
	}
}
