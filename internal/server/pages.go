package server

import (
	"encoding/json"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"

	"onlyoffice-fnos/internal/file"
	"onlyoffice-fnos/internal/format"
	"onlyoffice-fnos/web"
)

// EditorPageData holds data for the editor page template
type EditorPageData struct {
	Title         string
	ConfigJSON    template.JS
	DocServerPath string // Frontend path for loading JS (e.g., "/doc-svr")
	Lang          string
}

// ConvertPageData holds data for the convert page template
type ConvertPageData struct {
	FileName        string
	FilePath        string
	FilePathEncoded string
	SourceFormat    string
	TargetFormat    string
	CanDirectEdit   bool
	IsCSV           bool // Show CSV encoding/delimiter selectors
	IsTXT           bool // Show TXT encoding selector (txt→docx only)
	Error           string
}

// ErrorPageData holds data for the error page template
type ErrorPageData struct {
	Title     string
	Message   string
	ErrorCode string
	Details   string
	RetryURL  string
	BackURL   string
	BackText  string
}

// templates holds parsed templates
type templates struct {
	editor  *template.Template
	convert *template.Template
	error   *template.Template
}

// loadTemplates loads all HTML templates from embedded filesystem
func (s *Server) loadTemplates() error {
	var err error

	s.templates = &templates{}

	s.templates.editor, err = template.ParseFS(web.Templates, "templates/editor.tmpl")
	if err != nil {
		return err
	}

	s.templates.convert, err = template.ParseFS(web.Templates, "templates/convert.tmpl")
	if err != nil {
		return err
	}

	s.templates.error, err = template.ParseFS(web.Templates, "templates/error.tmpl")
	if err != nil {
		return err
	}

	return nil
}

// handleEditorPage handles GET /editor - renders the editor page
func (s *Server) handleEditorPage(w http.ResponseWriter, r *http.Request) {
	// Get file path from query parameter
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		s.renderErrorPage(w, &ErrorPageData{
			Title:   "参数错误",
			Message: "未指定文件路径",
		})
		return
	}

	// Get view mode
	mode := r.URL.Query().Get("mode")

	// Check settings
	if s.settings == nil {
		s.renderErrorPage(w, &ErrorPageData{
			Title:   "配置错误",
			Message: "应用配置未初始化，请通过 fnOS 应用设置进行配置。",
		})
		return
	}

	// Check if baseURL is configured (required for callbacks)
	effectiveBaseURL := s.getEffectiveBaseURL()
	if effectiveBaseURL == "" || effectiveBaseURL == "http://localhost:10099" {
		if s.settings.BaseURL == "" {
			s.renderErrorPage(w, &ErrorPageData{
				Title:   "配置错误",
				Message: "本机回调地址未配置，请通过 fnOS 应用设置进行配置。",
			})
			return
		}
	}

	// Get file info
	fileInfo, err := s.fileService.GetFileInfo(filePath)
	if err != nil {
		log.Printf("Failed to get file info: %v", err)
		errMsg := "无法获取文件信息"
		if err == file.ErrFileNotFound {
			errMsg = "文件不存在"
		}
		s.renderErrorPage(w, &ErrorPageData{
			Title:   "文件错误",
			Message: errMsg,
		})
		return
	}

	// Check if format needs conversion
	if s.formatManager.IsConvertible(fileInfo.Extension) && mode != "view" {
		// Redirect to convert page
		http.Redirect(w, r, "/convert?path="+url.QueryEscape(filePath), http.StatusFound)
		return
	}

	// Get user info from query or use defaults
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "fnos_user"
	}
	userName := r.URL.Query().Get("user_name")
	if userName == "" {
		userName = "fnOS 用户"
	}

	// Get language
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "zh"
	}

	// Build editor config
	configReq := &editorConfigRequest{
		FilePath:  filePath,
		FileInfo:  fileInfo,
		UserID:    userID,
		UserName:  userName,
		Lang:      lang,
		BaseURL:   s.baseURL,
		JWTSecret: s.settings.DocumentServerSecret,
		ViewMode:  mode == "view",
	}

	editorConfig, err := s.buildEditorConfig(configReq)
	if err != nil {
		log.Printf("Failed to build editor config: %v", err)
		s.renderErrorPage(w, &ErrorPageData{
			Title:   "配置错误",
			Message: "无法生成编辑器配置",
			Details: err.Error(),
		})
		return
	}

	// Convert config to JSON
	configJSON, err := json.Marshal(editorConfig)
	if err != nil {
		log.Printf("Failed to marshal editor config: %v", err)
		s.renderErrorPage(w, &ErrorPageData{
			Title:   "内部错误",
			Message: "无法序列化编辑器配置",
		})
		return
	}

	data := &EditorPageData{
		Title:         fileInfo.Name,
		ConfigJSON:    template.JS(configJSON),
		DocServerPath: s.getDocServerFrontendPath(),
		Lang:          lang,
	}

	// If templates are loaded, use them
	if s.templates != nil && s.templates.editor != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.templates.editor.Execute(w, data); err != nil {
			log.Printf("Failed to render editor template: %v", err)
			s.renderErrorPage(w, &ErrorPageData{
				Title:   "渲染错误",
				Message: "无法渲染编辑器页面",
			})
		}
		return
	}

	// Fallback to inline HTML
	s.renderEditorPageFallback(w, data)
}

// handleConvertPage handles GET /convert - renders the convert page
func (s *Server) handleConvertPage(w http.ResponseWriter, r *http.Request) {
	// Get file path from query parameter
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		s.renderErrorPage(w, &ErrorPageData{
			Title:   "参数错误",
			Message: "未指定文件路径",
		})
		return
	}

	// Get file info
	fileInfo, err := s.fileService.GetFileInfo(filePath)
	if err != nil {
		log.Printf("Failed to get file info: %v", err)
		errMsg := "无法获取文件信息"
		if err == file.ErrFileNotFound {
			errMsg = "文件不存在"
		}
		s.renderErrorPage(w, &ErrorPageData{
			Title:   "文件错误",
			Message: errMsg,
		})
		return
	}

	// Get target format
	targetFormat := s.formatManager.GetConvertTarget(fileInfo.Extension)
	if targetFormat == "" {
		// Not convertible, redirect to editor
		http.Redirect(w, r, "/editor?path="+url.QueryEscape(filePath), http.StatusFound)
		return
	}

	data := &ConvertPageData{
		FileName:        fileInfo.Name,
		FilePath:        filePath,
		FilePathEncoded: url.QueryEscape(filePath),
		SourceFormat:    fileInfo.Extension,
		TargetFormat:    targetFormat,
		CanDirectEdit:   false,
		IsCSV:           strings.EqualFold(fileInfo.Extension, "csv"),
		IsTXT:           strings.EqualFold(fileInfo.Extension, "txt") && targetFormat == "docx",
	}

	// If templates are loaded, use them
	if s.templates != nil && s.templates.convert != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.templates.convert.Execute(w, data); err != nil {
			log.Printf("Failed to render convert template: %v", err)
			s.renderErrorPage(w, &ErrorPageData{
				Title:   "渲染错误",
				Message: "无法渲染转换页面",
			})
		}
		return
	}

	// Fallback to inline HTML
	s.renderConvertPageFallback(w, data)
}

// renderErrorPage renders the error page
func (s *Server) renderErrorPage(w http.ResponseWriter, data *ErrorPageData) {
	if data.Title == "" {
		data.Title = "错误"
	}

	// If templates are loaded, use them
	if s.templates != nil && s.templates.error != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.templates.error.Execute(w, data); err != nil {
			log.Printf("Failed to render error template: %v", err)
			http.Error(w, data.Message, http.StatusInternalServerError)
		}
		return
	}

	// Fallback to inline HTML
	s.renderErrorPageFallback(w, data)
}

// editorConfigRequest holds parameters for building editor config
type editorConfigRequest struct {
	FilePath  string
	FileInfo  *file.FileInfo
	UserID    string
	UserName  string
	Lang      string
	BaseURL   string
	JWTSecret string
	ViewMode  bool
}

// buildEditorConfig builds the editor configuration
func (s *Server) buildEditorConfig(req *editorConfigRequest) (map[string]interface{}, error) {
	// Get format information
	formatInfo, ok := s.formatManager.GetFormat(req.FileInfo.Extension)
	if !ok {
		return nil, format.ErrFormatNotSupported
	}

	// Determine edit mode
	canEdit := formatInfo.Editable && !req.ViewMode
	mode := "view"
	if canEdit {
		mode = "edit"
	}

	// Generate document key
	docKey := s.configBuilder.GetDocumentKey(req.FilePath, req.FileInfo.ModTime)

	// Build download URL
	downloadURL := s.buildDownloadURL(req.FilePath)

	// Build callback URL
	callbackURL := s.buildCallbackURL(req.FilePath)

	config := map[string]interface{}{
		"document": map[string]interface{}{
			"fileType": req.FileInfo.Extension,
			"key":      docKey,
			"title":    req.FileInfo.Name,
			"url":      downloadURL,
			"permissions": map[string]interface{}{
				"edit":     canEdit,
				"download": true,
				"print":    true,
			},
		},
		"documentType": formatInfo.Type,
		"editorConfig": map[string]interface{}{
			"callbackUrl": callbackURL,
			"lang":        req.Lang,
			"mode":        mode,
			"user": map[string]interface{}{
				"id":   req.UserID,
				"name": req.UserName,
			},
		},
	}

	// Sign the configuration with JWT if secret is provided
	if req.JWTSecret != "" {
		token, err := s.jwtManager.Sign(req.JWTSecret, config)
		if err != nil {
			return nil, err
		}
		config["token"] = token
	}

	return config, nil
}

// buildCallbackURL builds the callback URL for a file
func (s *Server) buildCallbackURL(filePath string) string {
	baseURL := s.getEffectiveBaseURL()
	return baseURL + "/callback?path=" + url.QueryEscape(filePath)
}

// Fallback renderers for when templates are not available

func (s *Server) renderEditorPageFallback(w http.ResponseWriter, data *EditorPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>` + data.Title + ` - OnlyOffice Editor</title>
    <style>
        html, body { height: 100%; margin: 0; overflow: hidden; }
        #editor-container { width: 100%; height: 100%; }
    </style>
</head>
<body>
    <div id="editor-container"></div>
    <script src="` + data.DocServerPath + `/web-apps/apps/api/documents/api.js"></script>
    <script>new DocsAPI.DocEditor("editor-container", ` + string(data.ConfigJSON) + `);</script>
</body>
</html>`
	w.Write([]byte(html))
}

func (s *Server) renderConvertPageFallback(w http.ResponseWriter, data *ConvertPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	textOptions := ""
	if data.IsCSV {
		textOptions = `
    <div style="text-align:left; margin: 10px 0;">
        <label for="codePage" style="display:block; margin-bottom:4px; font-weight:bold;">编码</label>
        <select id="codePage" name="codePage" style="width:100%; padding:8px; margin-bottom:10px;">
            <option value="65001" selected>UTF-8（推荐）</option>
            <option value="936">简体中文 GBK / GB2312</option>
            <option value="950">繁体中文 Big5</option>
        </select>
        <label for="delimiter" style="display:block; margin-bottom:4px; font-weight:bold;">分隔符</label>
        <select id="delimiter" name="delimiter" style="width:100%; padding:8px;">
            <option value="4" selected>逗号 (,)</option>
            <option value="1">Tab</option>
            <option value="2">分号 (;)</option>
        </select>
    </div>`
	} else if data.IsTXT {
		textOptions = `
    <div style="text-align:left; margin: 10px 0;">
        <label for="codePage" style="display:block; margin-bottom:4px; font-weight:bold;">编码</label>
        <select id="codePage" name="codePage" style="width:100%; padding:8px;">
            <option value="65001" selected>UTF-8（推荐）</option>
            <option value="936">简体中文 GBK / GB2312</option>
            <option value="950">繁体中文 Big5</option>
        </select>
    </div>`
	}
	html := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>格式转换</title>
    <script src="/static/htmx.min.js"></script>
    <style>
        body { font-family: sans-serif; max-width: 500px; margin: 40px auto; padding: 20px; }
        .btn { display: block; width: 100%; padding: 12px; margin: 10px 0; text-align: center; border: none; border-radius: 4px; cursor: pointer; text-decoration: none; }
        .btn-primary { background: #4a90d9; color: white; }
        .btn-secondary { background: #f0f0f0; color: #333; }
        .modal { display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.5); z-index: 1000; }
        .modal.is-active { display: flex; align-items: center; justify-content: center; }
        .modal-card { background: white; border-radius: 8px; max-width: 400px; width: 90%; }
        .modal-card-head { padding: 16px; border-bottom: 1px solid #ddd; display: flex; justify-content: space-between; align-items: center; }
        .modal-card-body { padding: 16px; }
        .modal-card-foot { padding: 16px; border-top: 1px solid #ddd; display: flex; gap: 8px; justify-content: flex-end; }
        .modal-actions { flex-wrap: wrap; display: flex; }
        .modal-actions .btn { flex: 1 1 auto; width: auto; margin: 0; min-width: 120px; padding: 12px; }
        @media screen and (max-width: 480px) {
            .modal-actions { flex-direction: column; }
            .modal-actions .btn { width: 100%; }
        }
        .delete { background: none; border: none; font-size: 20px; cursor: pointer; }
    </style>
</head>
<body>
    <h1>格式转换</h1>
    <p>文件: ` + data.FileName + `</p>
    <p>格式: ` + data.SourceFormat + ` → ` + data.TargetFormat + `</p>
    <div id="error"></div>
    <form hx-post="/convert" hx-target="#error" hx-swap="innerHTML">
        <input type="hidden" name="path" value="` + data.FilePath + `">` + textOptions + `
        <button type="submit" class="btn btn-primary">转换为 ` + data.TargetFormat + ` 并编辑</button>
    </form>
    <a href="/editor?path=` + data.FilePathEncoded + `&mode=view" class="btn btn-secondary">以只读模式查看</a>

    <div class="modal" id="conflict-modal" onclick="if (event.target === this) cancelModal();">
        <div class="modal-card">
            <div class="modal-card-head">
                <strong id="modal-title">目标文件已存在</strong>
                <button class="delete" onclick="cancelModal()">&times;</button>
            </div>
            <div class="modal-card-body">
                <p id="modal-message">目标文件已存在，请选择操作：</p>
            </div>
            <div class="modal-card-foot modal-actions" id="modal-actions">
                <button class="btn btn-secondary" onclick="cancelModal()">取消</button>
                <button class="btn" style="background:#ffdd57;" onclick="submitOverwrite()">覆盖</button>
                <button class="btn" style="background:#48c774;color:white;" onclick="submitAutoRename()">生成带后缀文件</button>
            </div>
        </div>
    </div>

    <script>
        let currentSourcePath = '';
        let compatConfirmed = false;
        let pendingConflictPolicy = 'default'; // 'default' | 'autoRename' | 'overwrite'

        function resetCompatState() {
            compatConfirmed = false;
            pendingConflictPolicy = 'default';
        }

        function getSourcePath(resp) {
            if (resp && resp.sourcePath) return resp.sourcePath;
            const input = document.querySelector('input[name="path"]');
            return input ? input.value : '';
        }

        // Text conversion selectors only exist on CSV/TXT conversion pages.
        // CSV has codePage + delimiter; TXT has codePage only.
        function textConversionParams() {
            const params = {};
            const codePage = document.getElementById('codePage');
            if (codePage) params.codePage = codePage.value;
            const delimiter = document.getElementById('delimiter');
            if (delimiter) params.delimiter = delimiter.value;
            return params;
        }

        document.querySelector('form[hx-post="/convert"]').addEventListener('submit', resetCompatState);

        document.body.addEventListener('htmx:afterRequest', function(evt) {
            if (evt.detail.successful) {
                resetCompatState();
            }
        });

        document.body.addEventListener('htmx:responseError', function(evt) {
            let resp;
            try {
                resp = JSON.parse(evt.detail.xhr.responseText);
            } catch (e) {
                return;
            }
            if (!resp) return;

            if (resp.reason === 'atomic_no_replace_unsupported') {
                currentSourcePath = getSourcePath(resp);
                openCompatibilityModal();
                document.getElementById('error').innerHTML = '';
                evt.preventDefault();
            } else if (resp.conflict) {
                currentSourcePath = getSourcePath(resp);
                openConflictModal(resp.targetPath || '');
                document.getElementById('error').innerHTML = '';
                evt.preventDefault();
            }
        });

        function closeConflictModal() {
            document.getElementById('conflict-modal').classList.remove('is-active');
        }

        function cancelModal() {
            resetCompatState();
            closeConflictModal();
        }

        function openConflictModal(targetPath) {
            document.getElementById('modal-title').textContent = '目标文件已存在';
            document.getElementById('modal-message').textContent =
                (targetPath ? '"' + targetPath + '" ' : '') + '已存在，请选择操作：';
            document.getElementById('modal-actions').innerHTML =
                '<button class="btn btn-secondary" onclick="cancelModal()">取消</button>' +
                '<button class="btn" style="background:#ffdd57;" onclick="submitOverwrite()">覆盖</button>' +
                '<button class="btn" style="background:#48c774;color:white;" onclick="submitAutoRename()">生成带后缀文件</button>';
            document.getElementById('conflict-modal').classList.add('is-active');
        }

        function openCompatibilityModal() {
            document.getElementById('modal-title').textContent = '需要确认兼容模式';
            document.getElementById('modal-message').innerHTML =
                '当前系统无法使用原子无替换保存。继续将以兼容模式保存转换后的文件，' +
                '该模式无法保证：如果转换过程中有其他用户创建了同名文件，可能会造成冲突或覆盖。<br><br>' +
                '是否继续使用兼容模式？';
            document.getElementById('modal-actions').innerHTML =
                '<button class="btn btn-secondary" onclick="cancelModal()">取消</button>' +
                '<button class="btn" style="background:#4a90d9;color:white;" onclick="submitCompatibilityMode()">继续使用兼容模式</button>';
            document.getElementById('conflict-modal').classList.add('is-active');
        }

        function submitOverwrite() {
            pendingConflictPolicy = 'overwrite';
            closeConflictModal();
            htmx.ajax('POST', '/convert?overwrite=true', {
                target: '#error',
                swap: 'innerHTML',
                values: Object.assign({ path: currentSourcePath }, textConversionParams())
            });
        }

        function submitAutoRename() {
            pendingConflictPolicy = 'autoRename';
            closeConflictModal();
            const params = compatConfirmed ? '?auto_rename=true&allow_unsafe_save=true' : '?auto_rename=true';
            htmx.ajax('POST', '/convert' + params, {
                target: '#error',
                swap: 'innerHTML',
                values: Object.assign({ path: currentSourcePath }, textConversionParams())
            });
        }

        function submitCompatibilityMode() {
            compatConfirmed = true;
            closeConflictModal();

            // Retry the last user conflict choice, falling back to a plain compat confirmation.
            let params;
            if (pendingConflictPolicy === 'autoRename') {
                params = '?auto_rename=true&allow_unsafe_save=true';
            } else if (pendingConflictPolicy === 'overwrite') {
                params = '?overwrite=true';
            } else {
                params = '?allow_unsafe_save=true';
            }

            htmx.ajax('POST', '/convert' + params, {
                target: '#error',
                swap: 'innerHTML',
                values: Object.assign({ path: currentSourcePath }, textConversionParams())
            });
        }
    </script>
</body>
</html>`
	w.Write([]byte(html))
}

func (s *Server) renderErrorPageFallback(w http.ResponseWriter, data *ErrorPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>` + data.Title + `</title>
    <style>
        body { font-family: sans-serif; max-width: 500px; margin: 40px auto; padding: 20px; text-align: center; }
        .error-box { background: #f8d7da; color: #721c24; padding: 20px; border-radius: 8px; margin: 20px 0; }
    </style>
</head>
<body>
    <h1>` + data.Title + `</h1>
    <div class="error-box">` + data.Message + `</div>
</body>
</html>`
	w.Write([]byte(html))
}

// getDocServerFrontendPath returns the frontend path for Document Server JS
// This is a relative path that the browser will resolve against the current host
func (s *Server) getDocServerFrontendPath() string {
	if s.settings != nil && s.settings.DocServerPath != "" {
		return s.settings.DocServerPath
	}
	// Default to /doc-svr if not configured
	return "/doc-svr"
}

// getDocServerURL returns the appropriate Document Server URL based on request origin
// If both internal and public URLs are configured, it chooses based on whether the request
// comes from an internal network (private IP) or external network (domain name)
func (s *Server) getDocServerURL(r *http.Request) string {
	// If only one URL is configured, use it directly
	hasInternal := s.settings.DocumentServerURL != ""
	hasPublic := s.settings.DocumentServerPubURL != ""

	if hasInternal && !hasPublic {
		return s.settings.DocumentServerURL
	}
	if hasPublic && !hasInternal {
		return s.settings.DocumentServerPubURL
	}

	// Both URLs configured - determine which to use based on request origin
	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}
	// Remove any path suffix
	if idx := strings.Index(host, "/"); idx != -1 {
		host = host[:idx]
	}
	// Remove port for comparison
	hostWithoutPort := host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		hostWithoutPort = host[:idx]
	}

	// Check if request is from internal network
	if isInternalHost(hostWithoutPort) {
		return s.settings.DocumentServerURL
	}

	// External request - use public URL
	return s.settings.DocumentServerPubURL
}

// isInternalHost checks if the host looks like an internal/local address
func isInternalHost(host string) bool {
	// Check for localhost
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}

	// Check if it's an IP address
	ip := net.ParseIP(host)
	if ip != nil {
		// Check for private IP ranges
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}

	// It's a domain name - assume external
	return false
}
