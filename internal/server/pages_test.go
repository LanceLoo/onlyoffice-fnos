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

// textRetryMergeSnippet is the exact JS expression that forwards the text
// conversion (CSV/TXT) selection on overwrite, auto-rename, and
// compatibility-mode retry requests.
const textRetryMergeSnippet = `Object.assign({ path: currentSourcePath }, textConversionParams())`

// assertTextRetryPaths asserts that all three retry paths (overwrite,
// auto-rename, compatibility mode) forward the text conversion selection.
func assertTextRetryPaths(t *testing.T, body string) {
	t.Helper()
	if count := strings.Count(body, textRetryMergeSnippet); count != 3 {
		t.Fatalf("expected %q on all 3 retry paths, found %d occurrences", textRetryMergeSnippet, count)
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
		`<option value="65001" selected>UTF-8（推荐）</option>`,
		`<option value="936">简体中文 GBK / GB2312</option>`,
		`<option value="950">繁体中文 Big5</option>`,
		`name="delimiter"`,
		`<option value="4" selected>逗号 (,)</option>`,
		`<option value="1">Tab</option>`,
		`<option value="2">分号 (;)</option>`,
		// Retry paths must forward the selected CSV parameters.
		`textConversionParams()`,
	)
	assertTextRetryPaths(t, body)
}

func TestConvertPageShowsTXTOptionsWithDefaults(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "test.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	server := createPageTestServer(t, tempDir)

	body := getConvertPage(t, server, "test.txt")
	// TXT shows only the encoding selector with UTF-8 as the HTML default.
	assertContainsAll(t, body,
		`name="codePage"`,
		`<option value="65001" selected>UTF-8（推荐）</option>`,
		`<option value="936">简体中文 GBK / GB2312</option>`,
		`<option value="950">繁体中文 Big5</option>`,
		// Retry paths must forward the selected TXT parameters.
		`textConversionParams()`,
	)
	assertTextRetryPaths(t, body)
	// TXT must never show a delimiter selector.
	if strings.Contains(body, `name="delimiter"`) {
		t.Fatal("TXT page unexpectedly contains a delimiter selector")
	}
}

func TestConvertPageHidesTextOptionsForNonText(t *testing.T) {
	for _, fileName := range []string{"test.xls", "test.doc", "test.rtf", "test.odt"} {
		t.Run(fileName, func(t *testing.T) {
			tempDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tempDir, fileName), []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			server := createPageTestServer(t, tempDir)

			body := getConvertPage(t, server, fileName)
			for _, snippet := range []string{`name="codePage"`, `name="delimiter"`} {
				if strings.Contains(body, snippet) {
					t.Fatalf("non-text page unexpectedly contains %q", snippet)
				}
			}
			// Retry helper stays harmless without the selectors.
			assertContainsAll(t, body, `textConversionParams()`)
		})
	}
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
		`<option value="65001" selected>UTF-8（推荐）</option>`,
		`<option value="936">简体中文 GBK / GB2312</option>`,
		`<option value="950">繁体中文 Big5</option>`,
		`name="delimiter"`,
		`<option value="4" selected>逗号 (,)</option>`,
	)
	assertTextRetryPaths(t, body)

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
			t.Fatalf("non-text fallback page unexpectedly contains %q", snippet)
		}
	}
}

func TestConvertPageFallbackTXTOptions(t *testing.T) {
	server := &Server{}

	rec := httptest.NewRecorder()
	server.renderConvertPageFallback(rec, &ConvertPageData{
		FileName:        "test.txt",
		FilePath:        "test.txt",
		FilePathEncoded: "test.txt",
		SourceFormat:    "txt",
		TargetFormat:    "docx",
		IsTXT:           true,
	})
	body := rec.Body.String()
	assertContainsAll(t, body,
		`name="codePage"`,
		`<option value="65001" selected>UTF-8（推荐）</option>`,
		`<option value="936">简体中文 GBK / GB2312</option>`,
		`<option value="950">繁体中文 Big5</option>`,
	)
	assertTextRetryPaths(t, body)
	if strings.Contains(body, `name="delimiter"`) {
		t.Fatal("TXT fallback page unexpectedly contains a delimiter selector")
	}

	// A docx-targeting word format (e.g. doc) must not be treated as TXT.
	rec = httptest.NewRecorder()
	server.renderConvertPageFallback(rec, &ConvertPageData{
		FileName:        "test.doc",
		FilePath:        "test.doc",
		FilePathEncoded: "test.doc",
		SourceFormat:    "doc",
		TargetFormat:    "docx",
	})
	body = rec.Body.String()
	for _, snippet := range []string{`name="codePage"`, `name="delimiter"`} {
		if strings.Contains(body, snippet) {
			t.Fatalf("doc fallback page unexpectedly contains %q", snippet)
		}
	}
}
