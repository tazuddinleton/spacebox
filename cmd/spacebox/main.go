package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"database/sql"
	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	_ "modernc.org/sqlite"
)

const gmailScopes = "https://www.googleapis.com/auth/gmail.modify"

type server struct {
	db          *sql.DB
	oauth       *oauth2.Config
	tokenPath   string
	accountsDir string
	stateMu     sync.Mutex
	oauthState  map[string]time.Time
}

type thread struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Snippet string `json:"snippet"`
	Date    string `json:"date"`
	Unread  bool   `json:"unread"`
}

type threadPage struct {
	Threads       []thread `json:"threads"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
	Total         int64    `json:"total"`
	Unread        int64    `json:"unread"`
}

type messageDetail struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Date string `json:"date"`
	Body string `json:"body"`
}

type conversation struct {
	ID       string          `json:"id"`
	Subject  string          `json:"subject"`
	Messages []messageDetail `json:"messages"`
}

const threadCacheTTL = 5 * time.Minute

func main() {
	port := env("SPACEBOX_PORT", "8787")
	dbPath := env("SPACEBOX_DB", filepath.Join(dataDir(), "spacebox.db"))
	tokenPath := env("GMAIL_TOKEN_PATH", filepath.Join(dataDir(), "gmail-token.json"))

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sync_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gmail_accounts (email TEXT PRIMARY KEY, token_path TEXT NOT NULL)`); err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gmail_thread_cache (
		account_email TEXT PRIMARY KEY,
		payload TEXT NOT NULL,
		cached_at TEXT NOT NULL
	)`); err != nil {
		log.Fatal(err)
	}

	s := &server{
		db:          db,
		tokenPath:   tokenPath,
		accountsDir: filepath.Join(filepath.Dir(tokenPath), "gmail-accounts"),
		oauthState:  make(map[string]time.Time),
	}
	if config, err := gmailConfig(); err == nil {
		s.oauth = config
	} else {
		log.Printf("Gmail OAuth disabled: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/healthz", s.health)
	r.Get("/auth/gmail", s.gmailAuth)
	r.Get("/auth/gmail/callback", s.gmailCallback)
	r.Get("/api/accounts", s.accounts)
	r.Get("/api/threads", s.listThreads)
	r.Get("/api/threads/{id}", s.getThread)
	r.Post("/api/threads/{id}/reply", s.reply)
	r.Post("/api/sync", s.sync)
	r.Handle("/*", http.FileServer(http.Dir(env("SPACEBOX_WEB_DIR", "."))))

	log.Printf("Spacebox listening on http://127.0.0.1:%s", port)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, r))
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gmailConfigured": s.oauth != nil})
}

func (s *server) gmailAuth(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "Gmail OAuth is not configured", http.StatusNotImplemented)
		return
	}
	state, err := randomString(32)
	if err != nil {
		http.Error(w, "could not create OAuth state", http.StatusInternalServerError)
		return
	}
	s.stateMu.Lock()
	s.oauthState[state] = time.Now().Add(10 * time.Minute)
	s.stateMu.Unlock()
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "select_account")), http.StatusFound)
}

func (s *server) gmailCallback(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "Gmail OAuth is not configured", http.StatusNotImplemented)
		return
	}
	state := r.URL.Query().Get("state")
	if !s.validState(state) {
		http.Error(w, "invalid or expired OAuth state", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Gmail authorization was denied", http.StatusBadRequest)
		return
	}
	token, err := s.oauth.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "could not exchange Gmail authorization code", http.StatusBadGateway)
		return
	}
	client := s.oauth.Client(r.Context(), token)
	service, err := gmail.NewService(r.Context(), option.WithHTTPClient(client))
	if err != nil {
		http.Error(w, "could not connect to Gmail", http.StatusBadGateway)
		return
	}
	profile, err := service.Users.GetProfile("me").Do()
	if err != nil || profile.EmailAddress == "" {
		http.Error(w, "could not identify Gmail account", http.StatusBadGateway)
		return
	}
	accountID := profile.EmailAddress
	if err := os.MkdirAll(s.accountsDir, 0o700); err != nil {
		http.Error(w, "could not prepare account storage", http.StatusInternalServerError)
		return
	}
	if err := saveToken(s.accountTokenPath(accountID), token); err != nil {
		http.Error(w, "could not save Gmail token", http.StatusInternalServerError)
		return
	}
	if _, err := s.db.Exec(`INSERT INTO gmail_accounts(email, token_path) VALUES(?,?) ON CONFLICT(email) DO UPDATE SET token_path=excluded.token_path`, accountID, s.accountTokenPath(accountID)); err != nil {
		http.Error(w, "could not save Gmail account", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) accounts(w http.ResponseWriter, r *http.Request) {
	s.migrateLegacyAccount(r.Context())
	rows, err := s.db.Query(`SELECT email FROM gmail_accounts ORDER BY email`)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			writeError(w, err)
			return
		}
		out = append(out, map[string]string{"id": email, "email": email})
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) migrateLegacyAccount(ctx context.Context) {
	if s.oauth == nil {
		return
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM gmail_accounts`).Scan(&count); err != nil || count > 0 {
		return
	}
	token, err := loadToken(s.tokenPath)
	if err != nil {
		return
	}
	service, err := gmail.NewService(ctx, option.WithHTTPClient(s.oauth.Client(ctx, token)))
	if err != nil {
		return
	}
	profile, err := service.Users.GetProfile("me").Do()
	if err != nil || profile.EmailAddress == "" {
		return
	}
	if err := os.MkdirAll(s.accountsDir, 0o700); err != nil {
		return
	}
	accountPath := s.accountTokenPath(profile.EmailAddress)
	if err := saveToken(accountPath, token); err != nil {
		return
	}
	_, _ = s.db.Exec(`INSERT INTO gmail_accounts(email, token_path) VALUES(?,?) ON CONFLICT(email) DO NOTHING`, profile.EmailAddress, accountPath)
}

func (s *server) listThreads(w http.ResponseWriter, r *http.Request) {
	accountID := s.resolveAccount(r.URL.Query().Get("account"))
	w.Header().Set("X-Spacebox-Account", accountID)
	pageToken := r.URL.Query().Get("pageToken")
	if pageToken == "" && r.URL.Query().Get("refresh") != "1" {
		if cached, ok := s.cachedThreads(r.Context(), accountID); ok {
			w.Header().Set("X-Spacebox-Cache", "HIT")
			writeJSON(w, http.StatusOK, cached)
			return
		}
	}
	w.Header().Set("X-Spacebox-Cache", "MISS")
	service, err := s.gmail(r.Context(), accountID)
	if err != nil {
		writeError(w, err)
		return
	}
	call := service.Users.Threads.List("me").MaxResults(20)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	result, err := call.Do()
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]thread, 0, len(result.Threads))
	for _, item := range result.Threads {
		t, err := threadFromGmail(r.Context(), service, item.Id)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, t)
	}
	label, err := service.Users.Labels.Get("me", "INBOX").Do()
	if err != nil {
		writeError(w, err)
		return
	}
	page := threadPage{
		Threads: out, NextPageToken: result.NextPageToken,
		Total: label.ThreadsTotal, Unread: label.ThreadsUnread,
	}
	if pageToken == "" {
		s.cacheThreads(r.Context(), accountID, page)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) resolveAccount(accountID string) string {
	if accountID != "" {
		return accountID
	}
	var first string
	if err := s.db.QueryRow(`SELECT email FROM gmail_accounts ORDER BY email LIMIT 1`).Scan(&first); err == nil {
		return first
	}
	return ""
}

func (s *server) cachedThreads(ctx context.Context, accountID string) (threadPage, bool) {
	if accountID == "" {
		return threadPage{}, false
	}
	var payload, cachedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT payload, cached_at FROM gmail_thread_cache WHERE account_email = ?`, accountID).Scan(&payload, &cachedAt); err != nil {
		return threadPage{}, false
	}
	timestamp, err := time.Parse(time.RFC3339, cachedAt)
	if err != nil || time.Since(timestamp) > threadCacheTTL {
		return threadPage{}, false
	}
	var page threadPage
	if err := json.Unmarshal([]byte(payload), &page); err != nil {
		return threadPage{}, false
	}
	return page, true
}

func (s *server) cacheThreads(ctx context.Context, accountID string, page threadPage) {
	if accountID == "" {
		return
	}
	payload, err := json.Marshal(page)
	if err != nil {
		log.Printf("could not encode Gmail thread cache for %s: %v", accountID, err)
		return
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO gmail_thread_cache(account_email, payload, cached_at)
		VALUES(?,?,?) ON CONFLICT(account_email) DO UPDATE SET payload=excluded.payload, cached_at=excluded.cached_at`,
		accountID, payload, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		log.Printf("could not save Gmail thread cache for %s: %v", accountID, err)
	}
}

func (s *server) getThread(w http.ResponseWriter, r *http.Request) {
	service, err := s.gmail(r.Context(), r.URL.Query().Get("account"))
	if err != nil {
		writeError(w, err)
		return
	}
	item, err := service.Users.Threads.Get("me", chi.URLParam(r, "id")).Format("full").Do()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversationFromGmail(item))
}

func (s *server) reply(w http.ResponseWriter, r *http.Request) {
	service, err := s.gmail(r.Context(), r.URL.Query().Get("account"))
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.To == "" || input.Body == "" {
		writeErrorStatus(w, http.StatusBadRequest, "to and body are required")
		return
	}
	raw := fmt.Sprintf("To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n%s", input.To, input.Subject, input.Body)
	message := &gmail.Message{Raw: base64.RawURLEncoding.EncodeToString([]byte(raw))}
	sent, err := service.Users.Messages.Send("me", message).Do()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": sent.Id})
}

func (s *server) sync(w http.ResponseWriter, r *http.Request) {
	service, err := s.gmail(r.Context(), r.URL.Query().Get("account"))
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := service.Users.Threads.List("me").MaxResults(50).Do()
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO sync_state(key,value) VALUES('last_sync',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"threads": len(result.Threads)})
}

func (s *server) gmail(ctx context.Context, accountID string) (*gmail.Service, error) {
	if s.oauth == nil {
		return nil, errors.New("Gmail OAuth is not configured; set GMAIL_CLIENT_ID, GMAIL_CLIENT_SECRET, and GMAIL_REDIRECT_URL")
	}
	tokenPath := s.tokenPath
	if accountID != "" {
		tokenPath = s.accountTokenPath(accountID)
	} else {
		if first := s.resolveAccount(""); first != "" {
			tokenPath = s.accountTokenPath(first)
		}
	}
	token, err := loadToken(tokenPath)
	if err != nil {
		return nil, errors.New("Gmail is not connected; open /auth/gmail first")
	}
	client := s.oauth.Client(ctx, token)
	return gmail.NewService(ctx, option.WithHTTPClient(client))
}

func (s *server) accountTokenPath(email string) string {
	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return filepath.Join(s.accountsDir, fmt.Sprintf("%x.json", hash[:8]))
}

func threadFromGmail(ctx context.Context, service *gmail.Service, id string) (thread, error) {
	item, err := service.Users.Threads.Get("me", id).Format("metadata").MetadataHeaders("Subject", "From", "Date").Do()
	if err != nil {
		return thread{}, err
	}

	t := thread{ID: item.Id, Snippet: html.UnescapeString(item.Snippet)}
	if len(item.Messages) == 0 {
		return t, nil
	}

	last := item.Messages[len(item.Messages)-1]
	for _, h := range last.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "subject":
			t.Subject = h.Value
		case "from":
			t.From = h.Value
		case "date":
			t.Date = h.Value
		}
	}
	for _, label := range last.LabelIds {
		if label == "UNREAD" {
			t.Unread = true
			break
		}
	}
	return t, nil
}

func conversationFromGmail(item *gmail.Thread) conversation {
	out := conversation{ID: item.Id, Messages: make([]messageDetail, 0, len(item.Messages))}
	for _, message := range item.Messages {
		detail := messageDetail{ID: message.Id, Body: messageBody(message.Payload)}
		for _, header := range message.Payload.Headers {
			switch strings.ToLower(header.Name) {
			case "subject":
				if out.Subject == "" {
					out.Subject = header.Value
				}
			case "from":
				detail.From = header.Value
			case "to":
				detail.To = header.Value
			case "date":
				detail.Date = header.Value
			}
		}
		out.Messages = append(out.Messages, detail)
	}
	return out
}

func messageBody(part *gmail.MessagePart) string {
	if part == nil {
		return ""
	}
	if part.MimeType == "text/plain" && part.Body != nil && part.Body.Data != "" {
		data, err := decodeGmailData(part.Body.Data)
		if err == nil {
			return string(data)
		}
	}
	for _, nested := range part.Parts {
		if body := messageBody(nested); body != "" {
			return body
		}
	}
	if part.MimeType == "text/html" && part.Body != nil && part.Body.Data != "" {
		data, err := decodeGmailData(part.Body.Data)
		if err == nil {
			return regexp.MustCompile(`<[^>]*>`).ReplaceAllString(html.UnescapeString(string(data)), " ")
		}
	}
	return ""
}

func decodeGmailData(value string) ([]byte, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return base64.URLEncoding.DecodeString(value)
	}
	return data, nil
}

func (s *server) validState(state string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	expires, ok := s.oauthState[state]
	delete(s.oauthState, state)
	return ok && time.Now().Before(expires)
}

func gmailConfig() (*oauth2.Config, error) {
	id, secret, redirect := os.Getenv("GMAIL_CLIENT_ID"), os.Getenv("GMAIL_CLIENT_SECRET"), os.Getenv("GMAIL_REDIRECT_URL")
	if id == "" || secret == "" || redirect == "" {
		return nil, errors.New("Gmail credentials are missing")
	}
	return &oauth2.Config{ClientID: id, ClientSecret: secret, Endpoint: google.Endpoint, RedirectURL: redirect, Scopes: []string{gmailScopes}}, nil
}

func saveToken(path string, token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func randomString(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func dataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "spacebox")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".spacebox"
	}
	return filepath.Join(home, ".local", "share", "spacebox")
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	writeErrorStatus(w, http.StatusBadGateway, err.Error())
}

func writeErrorStatus(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
