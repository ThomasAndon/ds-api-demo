package main

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const maxBody = 16 << 20

type app struct {
	db            *sql.DB
	webRoot       string
	adminPassword string
	key           [32]byte
	httpClient    *http.Client
	adminSessions sync.Map
}

type llmConfig struct {
	BaseURL           string  `json:"base_url"`
	APIKey            string  `json:"api_key,omitempty"`
	APIKeySet         bool    `json:"api_key_set"`
	Model             string  `json:"model"`
	SystemPrompt      string  `json:"-"`
	Temperature       float64 `json:"temperature"`
	AccessPassword    string  `json:"access_password,omitempty"`
	AccessPasswordSet bool    `json:"access_password_set"`
}

type sessionSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type adminSession struct{ expires time.Time }

func main() {
	dsn := mustEnv("MYSQL_DSN")
	password := mustEnv("ADMIN_PASSWORD")
	secret := mustEnv("CONFIG_SECRET")
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("database: %v", err)
	}
	if err := migrate(ctx, db); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	a := &app{
		db: db, webRoot: env("WEB_ROOT", "dist"), adminPassword: password,
		key:        sha256.Sum256([]byte(secret)),
		httpClient: &http.Client{Timeout: 5 * time.Minute},
	}
	if err := a.ensureAccessPassword(ctx, password); err != nil {
		log.Fatalf("initialize access password: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /api/access/login", a.accessLogin)
	mux.HandleFunc("GET /api/access/status", a.accessOnly(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	mux.HandleFunc("POST /api/access/logout", a.accessLogout)
	mux.HandleFunc("GET /api/sessions", a.accessOnly(a.listSessions))
	mux.HandleFunc("GET /api/sessions/{id}", a.accessOnly(a.getSession))
	mux.HandleFunc("PUT /api/sessions/{id}", a.accessOnly(a.putSession))
	mux.HandleFunc("PATCH /api/sessions/{id}", a.accessOnly(a.renameSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", a.accessOnly(a.deleteSession))
	mux.HandleFunc("POST /api/sessions/import", a.accessOnly(a.importSessions))
	mux.HandleFunc("POST /api/chat/completions", a.accessOnly(a.chat))
	mux.HandleFunc("POST /api/admin/login", a.adminLogin)
	mux.HandleFunc("POST /api/admin/logout", a.adminLogout)
	mux.HandleFunc("GET /api/admin/config", a.adminOnly(a.getConfig))
	mux.HandleFunc("PUT /api/admin/config", a.adminOnly(a.putConfig))
	mux.HandleFunc("POST /api/admin/models", a.adminOnly(a.listModels))
	mux.Handle("/admin/", http.StripPrefix("/admin/", http.FileServer(http.Dir(filepath.Join(a.webRoot, "admin")))))
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.Handle("/", spaHandler(a.webRoot))

	server := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:18082"), Handler: securityHeaders(requestLog(mux)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 6 * time.Minute, WriteTimeout: 6 * time.Minute, IdleTimeout: 60 * time.Second}
	log.Printf("ds-api-demo listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

func migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS llm_config (id TINYINT UNSIGNED NOT NULL PRIMARY KEY, base_url VARCHAR(1024) NOT NULL, api_key_enc TEXT NOT NULL, model VARCHAR(255) NOT NULL, system_prompt TEXT NOT NULL, temperature DECIMAL(4,3) NOT NULL DEFAULT 0.700, access_password_hash CHAR(64) NOT NULL DEFAULT '', updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS chat_sessions (id VARCHAR(80) NOT NULL PRIMARY KEY, title VARCHAR(255) NOT NULL DEFAULT '', session_json LONGTEXT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL, INDEX idx_chat_sessions_updated (updated_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE llm_config ADD COLUMN access_password_hash CHAR(64) NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "Duplicate column") {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT IGNORE INTO llm_config (id, base_url, api_key_enc, model, system_prompt, temperature, access_password_hash) VALUES (1, 'https://la-api.vxvxvx.top/v1', '', 'gpt-5.3-codex', '', 0.700, '')`); err != nil {
		return err
	}
	_, _ = db.ExecContext(ctx, `UPDATE llm_config SET base_url='https://la-api.vxvxvx.top/v1', model='gpt-5.3-codex', system_prompt='' WHERE api_key_enc='' AND base_url='https://api.deepseek.com/v1'`)
	return nil
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.db.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) listSessions(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,title,created_at,updated_at FROM chat_sessions ORDER BY updated_at DESC`)
	if err != nil {
		writeError(w, 500, "读取会话失败")
		return
	}
	defer rows.Close()
	items := []sessionSummary{}
	for rows.Next() {
		var s sessionSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			writeError(w, 500, "读取会话失败")
			return
		}
		items = append(items, s)
	}
	writeJSON(w, 200, items)
}

func validID(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, c := range id {
		if !(c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func (a *app) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, 400, "无效会话 ID")
		return
	}
	var raw string
	err := a.db.QueryRowContext(r.Context(), `SELECT session_json FROM chat_sessions WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "会话不存在")
		return
	}
	if err != nil {
		writeError(w, 500, "读取会话失败")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(w, raw)
}

func readJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func parseSession(raw json.RawMessage) (sessionSummary, error) {
	var s sessionSummary
	if len(raw) == 0 || !json.Valid(raw) {
		return s, errors.New("invalid json")
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	if !validID(s.ID) || s.CreatedAt <= 0 || s.UpdatedAt <= 0 {
		return s, errors.New("invalid session")
	}
	if len([]rune(s.Title)) > 255 {
		return s, errors.New("title too long")
	}
	return s, nil
}

func (a *app) putSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, 400, "会话数据过大")
		return
	}
	s, err := parseSession(raw)
	if err != nil || s.ID != id {
		writeError(w, 400, "无效会话数据")
		return
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO chat_sessions(id,title,session_json,created_at,updated_at) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE title=VALUES(title),session_json=VALUES(session_json),created_at=VALUES(created_at),updated_at=VALUES(updated_at)`, s.ID, s.Title, string(raw), s.CreatedAt, s.UpdatedAt)
	if err != nil {
		writeError(w, 500, "保存会话失败")
		return
	}
	w.WriteHeader(204)
}

func (a *app) renameSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Title string `json:"title"`
	}
	if !validID(id) || readJSON(r, &body) != nil || len([]rune(body.Title)) > 255 {
		writeError(w, 400, "无效标题")
		return
	}
	var raw string
	if err := a.db.QueryRowContext(r.Context(), `SELECT session_json FROM chat_sessions WHERE id=?`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "会话不存在")
		} else {
			writeError(w, 500, "读取会话失败")
		}
		return
	}
	var data map[string]any
	if json.Unmarshal([]byte(raw), &data) != nil {
		writeError(w, 500, "会话数据损坏")
		return
	}
	data["title"] = body.Title
	updated, _ := json.Marshal(data)
	if _, err := a.db.ExecContext(r.Context(), `UPDATE chat_sessions SET title=?,session_json=? WHERE id=?`, body.Title, string(updated), id); err != nil {
		writeError(w, 500, "重命名失败")
		return
	}
	w.WriteHeader(204)
}

func (a *app) deleteSession(w http.ResponseWriter, r *http.Request) {
	if !validID(r.PathValue("id")) {
		writeError(w, 400, "无效会话 ID")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM chat_sessions WHERE id=?`, r.PathValue("id")); err != nil {
		writeError(w, 500, "删除失败")
		return
	}
	w.WriteHeader(204)
}

func (a *app) importSessions(w http.ResponseWriter, r *http.Request) {
	var raws []json.RawMessage
	if readJSON(r, &raws) != nil {
		writeError(w, 400, "无效导入数据")
		return
	}
	if len(raws) > 10000 {
		writeError(w, 400, "会话数量过多")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "导入失败")
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM chat_sessions`); err != nil {
		writeError(w, 500, "导入失败")
		return
	}
	for _, raw := range raws {
		s, e := parseSession(raw)
		if e != nil {
			writeError(w, 400, "导入包含无效会话")
			return
		}
		if _, e = tx.ExecContext(r.Context(), `INSERT INTO chat_sessions(id,title,session_json,created_at,updated_at) VALUES(?,?,?,?,?)`, s.ID, s.Title, string(raw), s.CreatedAt, s.UpdatedAt); e != nil {
			writeError(w, 500, "导入失败")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "导入失败")
		return
	}
	w.WriteHeader(204)
}

type appMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}
type chatRequest struct {
	Model    string       `json:"model"`
	System   string       `json:"system"`
	Messages []appMessage `json:"messages"`
	Thinking struct {
		Type string `json:"type"`
	} `json:"thinking"`
	SafetyCheck map[string]any   `json:"safety_check"`
	Tools       []map[string]any `json:"tools"`
	Title       *struct {
		Enabled bool `json:"enabled"`
	} `json:"title"`
	Stream bool `json:"stream"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model         string          `json:"model"`
	Messages      []openAIMessage `json:"messages"`
	Temperature   float64         `json:"temperature"`
	Stream        bool            `json:"stream"`
	StreamOptions map[string]bool `json:"stream_options"`
}

// 官方前端会携带 DeepSeek 专用字段。这里只解析本项目需要的字段，其他字段兼容接收但不会透传。
func decodeChatRequest(reader io.Reader) (chatRequest, error) {
	var request chatRequest
	err := json.NewDecoder(reader).Decode(&request)
	return request, err
}

func textContent(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, part := range x {
			if m, ok := part.(map[string]any); ok {
				if t, _ := m["type"].(string); t == "text" {
					if s, _ := m["text"].(string); s != "" {
						if b.Len() > 0 {
							b.WriteByte('\n')
						}
						b.WriteString(s)
					}
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func buildOpenAIRequest(in chatRequest, cfg llmConfig) (openAIRequest, string, error) {
	messages := make([]openAIMessage, 0, len(in.Messages))
	lastUser := ""
	for _, message := range in.Messages {
		// latest_reminder、system、thinking、tools 等均不进入 OpenAI 上游消息。
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		content := textContent(message.Content)
		if content == "" {
			continue
		}
		messages = append(messages, openAIMessage{Role: message.Role, Content: content})
		if message.Role == "user" {
			lastUser = content
		}
	}
	if len(messages) == 0 {
		return openAIRequest{}, "", errors.New("no user or assistant text")
	}
	return openAIRequest{
		Model:         cfg.Model,
		Messages:      messages,
		Temperature:   cfg.Temperature,
		Stream:        true,
		StreamOptions: map[string]bool{"include_usage": true},
	}, lastUser, nil
}

func (a *app) chat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	in, err := decodeChatRequest(r.Body)
	if err != nil || len(in.Messages) == 0 {
		writeError(w, 400, "无效聊天请求")
		return
	}
	cfg, err := a.readConfig(r.Context())
	if err != nil {
		writeError(w, 500, "读取模型配置失败")
		return
	}
	if cfg.APIKey == "" {
		writeError(w, 503, "请先在 /admin 配置 API Key")
		return
	}
	if isOfficialDeepSeek(cfg.BaseURL) {
		a.chatDeepSeekNative(w, r, in, cfg)
		return
	}
	payload, lastUser, err := buildOpenAIRequest(in, cfg)
	if err != nil {
		writeError(w, 400, "没有可发送的文本")
		return
	}
	body, _ := json.Marshal(payload)
	endpoint, err := completionURL(cfg.BaseURL)
	if err != nil {
		writeError(w, 500, "后台 URL 配置无效")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		writeError(w, 500, "创建上游请求失败")
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		writeError(w, 502, "连接模型服务失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		writeError(w, resp.StatusCode, "模型服务返回错误: "+strings.TrimSpace(string(b)))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "服务器不支持流式响应")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	sendEvent(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": randomToken(12), "model": cfg.Model}})
	flusher.Flush()
	type upstreamChunk struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
			} `json:"delta"`
			FinishReason any `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	thinkingStarted, textStarted := false, false
	thinkingIndex, textIndex := 0, 1
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk upstreamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.Reasoning != "" {
			if !thinkingStarted {
				sendEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": thinkingIndex, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
				thinkingStarted = true
			}
			sendEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": thinkingIndex, "delta": map[string]any{"type": "thinking_delta", "thinking": d.Reasoning}})
		}
		if d.Content != "" {
			if thinkingStarted && !textStarted {
				sendEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": thinkingIndex})
			}
			if !textStarted {
				sendEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": textIndex, "content_block": map[string]any{"type": "text", "text": ""}})
				textStarted = true
			}
			sendEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": textIndex, "delta": map[string]any{"type": "text_delta", "text": d.Content}})
		}
		flusher.Flush()
	}
	if err := scanner.Err(); err != nil {
		return
	}
	if textStarted {
		sendEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": textIndex})
	} else if thinkingStarted {
		sendEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": thinkingIndex})
	}
	title := ""
	if in.Title != nil && in.Title.Enabled {
		title = strings.TrimSpace(lastUser)
		r := []rune(title)
		if len(r) > 28 {
			title = string(r[:28]) + "…"
		}
	}
	sendEvent(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil, "title": title}})
	sendEvent(w, "message_stop", map[string]any{"type": "message_stop"})
	flusher.Flush()
}

func isOfficialDeepSeek(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(u.Hostname(), "api.deepseek.com")
}

func deepSeekMessagesURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || !strings.EqualFold(u.Hostname(), "api.deepseek.com") {
		return "", errors.New("invalid DeepSeek URL")
	}
	u.Path = "/anthropic/v1/messages"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func buildDeepSeekNativeRequest(in chatRequest, cfg llmConfig) map[string]any {
	payload := map[string]any{
		"model":        cfg.Model,
		"messages":     in.Messages,
		"thinking":     in.Thinking,
		"safety_check": in.SafetyCheck,
		"tools":        in.Tools,
		"stream":       true,
	}
	if in.Title != nil {
		payload["title"] = in.Title
	}
	return payload
}

// DeepSeek 官方地址使用官方前端原生协议，让深度思考和智能搜索继续生效。
// system 被刻意省略；模型和 API Key 始终来自后台配置。
func (a *app) chatDeepSeekNative(w http.ResponseWriter, r *http.Request, in chatRequest, cfg llmConfig) {
	endpoint, err := deepSeekMessagesURL(cfg.BaseURL)
	if err != nil {
		writeError(w, 500, "后台 URL 配置无效")
		return
	}
	payload := buildDeepSeekNativeRequest(in, cfg)
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		writeError(w, 500, "创建上游请求失败")
		return
	}
	req.Header.Set("x-api-key", cfg.APIKey)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		writeError(w, 502, "连接模型服务失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		writeError(w, resp.StatusCode, "模型服务返回错误: "+strings.TrimSpace(string(b)))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "服务器不支持流式响应")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			return
		}
	}
}

func completionURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid url")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path += "/chat/completions"
	}
	return u.String(), nil
}
func sendEvent(w io.Writer, event string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func (a *app) readConfig(ctx context.Context) (llmConfig, error) {
	var c llmConfig
	var enc string
	var accessHash string
	err := a.db.QueryRowContext(ctx, `SELECT base_url,api_key_enc,model,system_prompt,temperature,access_password_hash FROM llm_config WHERE id=1`).Scan(&c.BaseURL, &enc, &c.Model, &c.SystemPrompt, &c.Temperature, &accessHash)
	if err != nil {
		return c, err
	}
	if enc != "" {
		c.APIKey, err = a.decrypt(enc)
		if err != nil {
			return c, err
		}
		c.APIKeySet = c.APIKey != ""
	}
	c.AccessPasswordSet = accessHash != ""
	return c, nil
}

func (a *app) adminLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if readJSON(r, &body) != nil || subtle.ConstantTimeCompare([]byte(body.Password), []byte(a.adminPassword)) != 1 {
		time.Sleep(300 * time.Millisecond)
		writeError(w, 401, "密码错误")
		return
	}
	token := randomToken(32)
	a.adminSessions.Store(token, adminSession{expires: time.Now().Add(12 * time.Hour)})
	http.SetCookie(w, &http.Cookie{Name: "ds_admin", Value: token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	w.WriteHeader(204)
}
func (a *app) adminLogout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("ds_admin"); e == nil {
		a.adminSessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "ds_admin", Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}
func (a *app) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("ds_admin")
		if err != nil {
			writeError(w, 401, "请先登录")
			return
		}
		v, ok := a.adminSessions.Load(c.Value)
		if !ok || time.Now().After(v.(adminSession).expires) {
			a.adminSessions.Delete(c.Value)
			writeError(w, 401, "登录已过期")
			return
		}
		next(w, r)
	}
}
func (a *app) getConfig(w http.ResponseWriter, r *http.Request) {
	c, err := a.readConfig(r.Context())
	if err != nil {
		writeError(w, 500, "读取配置失败")
		return
	}
	c.APIKey = ""
	c.AccessPassword = ""
	writeJSON(w, 200, c)
}
func (a *app) putConfig(w http.ResponseWriter, r *http.Request) {
	var c llmConfig
	if readJSON(r, &c) != nil {
		writeError(w, 400, "无效配置")
		return
	}
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.Model = strings.TrimSpace(c.Model)
	if _, err := completionURL(c.BaseURL); err != nil || c.Model == "" || len(c.Model) > 255 || c.Temperature < 0 || c.Temperature > 2 {
		writeError(w, 400, "请检查 URL、模型或温度")
		return
	}
	var enc string
	if c.APIKey == "" {
		if err := a.db.QueryRowContext(r.Context(), `SELECT api_key_enc FROM llm_config WHERE id=1`).Scan(&enc); err != nil {
			writeError(w, 500, "读取配置失败")
			return
		}
	} else {
		var err error
		enc, err = a.encrypt(strings.TrimSpace(c.APIKey))
		if err != nil {
			writeError(w, 500, "加密配置失败")
			return
		}
	}
	var accessHash string
	if err := a.db.QueryRowContext(r.Context(), `SELECT access_password_hash FROM llm_config WHERE id=1`).Scan(&accessHash); err != nil {
		writeError(w, 500, "读取配置失败")
		return
	}
	if strings.TrimSpace(c.AccessPassword) != "" {
		accessHash = a.hashPassword(strings.TrimSpace(c.AccessPassword))
	}
	if _, err := a.db.ExecContext(r.Context(), `UPDATE llm_config SET base_url=?,api_key_enc=?,model=?,system_prompt='',temperature=?,access_password_hash=? WHERE id=1`, c.BaseURL, enc, c.Model, c.Temperature, accessHash); err != nil {
		writeError(w, 500, "保存配置失败")
		return
	}
	w.WriteHeader(204)
}

func (a *app) accessLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if readJSON(r, &body) != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	candidate := a.hashPassword(strings.TrimSpace(body.Password))
	http.SetCookie(w, &http.Cookie{Name: "ds_access", Value: a.accessToken(candidate), Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) hashPassword(password string) string {
	mac := hmac.New(sha256.New, a.key[:])
	_, _ = mac.Write([]byte("password:" + password))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func (a *app) ensureAccessPassword(ctx context.Context, password string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE llm_config SET access_password_hash=? WHERE id=1 AND access_password_hash=''`, a.hashPassword(password))
	return err
}

func (a *app) accessLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "ds_access", Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) accessToken(passwordHash string) string {
	mac := hmac.New(sha256.New, a.key[:])
	_, _ = mac.Write([]byte("access:" + passwordHash))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *app) accessOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("ds_access")
		var stored string
		if err != nil || a.db.QueryRowContext(r.Context(), `SELECT access_password_hash FROM llm_config WHERE id=1`).Scan(&stored) != nil || stored == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(a.accessToken(stored))) != 1 {
			writeError(w, http.StatusBadGateway, "连接失败")
			return
		}
		next(w, r)
	}
}

func modelsURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid url")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/chat/completions")
	if !strings.HasSuffix(u.Path, "/models") {
		u.Path += "/models"
	}
	return u.String(), nil
}

func (a *app) listModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if readJSON(r, &body) != nil {
		writeError(w, 400, "参数无效")
		return
	}
	cfg, err := a.readConfig(r.Context())
	if err != nil {
		writeError(w, 500, "读取配置失败")
		return
	}
	if strings.TrimSpace(body.BaseURL) == "" {
		body.BaseURL = cfg.BaseURL
	}
	if strings.TrimSpace(body.APIKey) == "" {
		body.APIKey = cfg.APIKey
	}
	endpoint, err := modelsURL(body.BaseURL)
	if err != nil || body.APIKey == "" {
		writeError(w, 400, "请先填写 URL 和 API Key")
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(body.APIKey))
	resp, err := a.httpClient.Do(req)
	if err != nil {
		writeError(w, 502, "获取模型失败")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeError(w, 502, "获取模型失败")
		return
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result) != nil {
		writeError(w, 502, "模型列表格式错误")
		return
	}
	models := make([]string, 0, len(result.Data))
	for _, item := range result.Data {
		if strings.TrimSpace(item.ID) != "" {
			models = append(models, item.ID)
		}
	}
	sort.Strings(models)
	writeJSON(w, 200, map[string]any{"models": models})
}

func (a *app) encrypt(s string) (string, error) {
	block, err := aes.NewCipher(a.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(s), nil)
	return base64.RawStdEncoding.EncodeToString(out), nil
}
func (a *app) decrypt(s string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(a.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid secret")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(plain), err
}

func spaHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			writeError(w, 405, "method not allowed")
			return
		}
		clean := filepath.Clean(r.URL.Path)
		path := filepath.Join(root, clean)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("%s is required", k)
	}
	return v
}
