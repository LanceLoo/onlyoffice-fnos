package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"onlyoffice-fnos/internal/config"
	"onlyoffice-fnos/internal/editor"
	"onlyoffice-fnos/internal/file"
	"onlyoffice-fnos/internal/format"
	"onlyoffice-fnos/internal/jwt"
	"onlyoffice-fnos/web"
)

const (
	// RegularRequestTimeout bounds normal web and API requests.
	RegularRequestTimeout = 60 * time.Second
	// CallbackTimeout allows Document Server callbacks to download large documents.
	CallbackTimeout = 5 * time.Minute
)

// Server represents the HTTP server
type Server struct {
	router          *chi.Mux
	settings        *config.Settings
	fileService     *file.Service
	formatManager   *format.Manager
	jwtManager      *jwt.Manager
	configBuilder   *editor.ConfigBuilder
	baseURL         string
	templates       *templates
	callbackSecret  []byte
	callbackLocks   map[string]*callbackPathLock
	callbackLocksMu sync.Mutex
	now             func() time.Time
	documentClient  *http.Client
}

// Config holds server configuration
type Config struct {
	Settings      *config.Settings
	FileService   *file.Service
	FormatManager *format.Manager
	JWTManager    *jwt.Manager
	BaseURL       string
}

// New creates a new Server instance
func New(cfg *Config) *Server {
	s := &Server{
		router:         chi.NewRouter(),
		settings:       cfg.Settings,
		fileService:    cfg.FileService,
		formatManager:  cfg.FormatManager,
		jwtManager:     cfg.JWTManager,
		baseURL:        cfg.BaseURL,
		callbackLocks:  make(map[string]*callbackPathLock),
		now:            time.Now,
		documentClient: &http.Client{Timeout: CallbackTimeout},
	}
	if cfg.Settings != nil && cfg.Settings.DocumentServerSecret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.Settings.DocumentServerSecret))
		mac.Write([]byte("onlyoffice-fnos/callback-session/v1"))
		s.callbackSecret = mac.Sum(nil)
	} else {
		s.callbackSecret = make([]byte, sha256.Size)
		if _, err := rand.Read(s.callbackSecret); err != nil {
			panic("generate callback session secret: " + err.Error())
		}
		log.Printf("SECURITY WARNING: Document Server JWT is disabled; callback sessions use a process-local secret and production deployments must enable JWT with matching connector and Document Server secrets")
	}

	// Use baseURL from settings if available
	if cfg.Settings != nil && cfg.Settings.BaseURL != "" {
		s.baseURL = cfg.Settings.BaseURL
	}
	// If still empty after loading settings, use the provided config (command line default)
	if s.baseURL == "" && cfg.BaseURL != "" {
		s.baseURL = cfg.BaseURL
	}

	// Create config builder
	s.configBuilder = editor.NewConfigBuilder(cfg.FormatManager, cfg.JWTManager)

	// Load embedded templates
	if err := s.loadTemplates(); err != nil {
		log.Printf("Warning: failed to load templates: %v", err)
	}

	// Setup middleware
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	// Setup routes
	s.setupRoutes()

	return s
}

// setupRoutes configures all HTTP routes
func (s *Server) setupRoutes() {
	regular := s.router.With(middleware.Timeout(RegularRequestTimeout))
	callback := s.router.With(middleware.Timeout(CallbackTimeout))

	// Embedded static files
	staticFS, err := fs.Sub(web.Static, "static")
	if err != nil {
		log.Printf("Warning: failed to get static sub-filesystem: %v", err)
	} else {
		regular.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	}

	// Page routes
	regular.Get("/editor", s.handleEditorPage)
	regular.Get("/convert", s.handleConvertPage)

	// Document Server integration routes
	regular.Get("/download", s.handleDownload)
	callback.Post("/callback", s.handleCallback)
	regular.Post("/convert", s.handleConvert)
}

// Router returns the chi router for testing
func (s *Server) Router() *chi.Mux {
	return s.router
}

// ServeHTTP implements http.Handler
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Start starts the HTTP server
func (s *Server) Start(addr string) error {
	log.Printf("Starting server on %s", addr)
	return http.ListenAndServe(addr, s.router)
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	// Chi router doesn't have built-in shutdown, but we can use http.Server
	return nil
}

// JSON response helpers
func (s *Server) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

func (s *Server) respondError(w http.ResponseWriter, status int, message string) {
	s.respondJSON(w, status, map[string]interface{}{
		"error":   status,
		"message": message,
	})
}
