package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type M map[string]any
type queryer interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}
type App struct {
	db       *sql.DB
	demo     bool
	web      string
	origins  map[string]bool
	mu       sync.Mutex
	attempts map[string]attempt
}
type attempt struct {
	count int
	until time.Time
}
type apiError struct {
	code int
	msg  string
}

func (e apiError) Error() string { return e.msg }
func bad(s string) error         { return apiError{400, s} }
func now() string                { return time.Now().UTC().Format(time.RFC3339) }
func text(m M, k string) string  { v, _ := m[k].(string); return v }
func num(m M, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	}
	return 0
}
func integer(m M, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return int64(num(m, k))
}
func boolean(m M, k string) bool { v, _ := m[k].(bool); return v }
func randomToken(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func digest(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func passwordHash(p string) string {
	s := randomToken(16)
	k, _ := pbkdf2.Key(sha256.New, p, []byte(s), 600000, 32)
	return "pbkdf2-sha256:600000:" + s + ":" + hex.EncodeToString(k)
}
func passwordOK(p, h string) bool {
	parts := strings.Split(h, ":")
	iterations := 600000
	var salt, expected string
	if len(parts) == 4 && parts[0] == "pbkdf2-sha256" {
		n, e := strconv.Atoi(parts[1])
		if e != nil || n < 120000 || n > 1000000 {
			return false
		}
		iterations = n
		salt = parts[2]
		expected = parts[3]
	} else if len(parts) == 2 {
		iterations = 120000
		salt = parts[0]
		expected = parts[1]
	} else {
		return false
	}
	k, _ := pbkdf2.Key(sha256.New, p, []byte(salt), iterations, 32)
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(k)), []byte(expected)) == 1
}
func openApp(path string, demo bool) (*App, error) {
	if path != "" && path != ":memory:" {
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return nil, e
		}
	}
	db, e := sql.Open("sqlite3", path+"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	a := &App{db: db, demo: demo, web: os.Getenv("WEB_DIST"), origins: map[string]bool{}, attempts: map[string]attempt{}}
	if a.web == "" {
		a.web = "../web/dist"
	}
	for _, s := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			a.origins[s] = true
		}
	}
	if demo {
		a.origins["http://localhost:5173"] = true
		a.origins["http://127.0.0.1:5173"] = true
	}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT,email TEXT NOT NULL UNIQUE,name TEXT NOT NULL,password TEXT NOT NULL,role TEXT NOT NULL DEFAULT 'user',status TEXT NOT NULL DEFAULT 'active',balance INTEGER NOT NULL DEFAULT 0 CHECK(balance>=0),created_at TEXT NOT NULL,subscription_token TEXT NOT NULL UNIQUE,invite_code TEXT NOT NULL UNIQUE,inviter_id INTEGER NOT NULL DEFAULT 0); CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,expires_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS entities (id INTEGER PRIMARY KEY AUTOINCREMENT,kind TEXT NOT NULL,user_id INTEGER NOT NULL DEFAULT 0,data TEXT NOT NULL); CREATE INDEX IF NOT EXISTS entities_kind_user ON entities(kind,user_id); CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	if e = a.initTraffic(); e != nil {
		db.Close()
		return nil, e
	}
	defaults := map[string]string{"site_name": "NiceCloud", "site_description": "订阅与节点服务平台", "contact_email": "support@example.test", "invite_rate": "0.10", "registration_enabled": "true", "payment_status": "not_connected", "node_sync_status": "not_connected", "speed_enforcement": "not_connected", "device_enforcement": "not_connected"}
	for k, v := range defaults {
		if _, e = db.Exec("INSERT OR IGNORE INTO settings(key,value)VALUES(?,?)", k, v); e != nil {
			return nil, e
		}
	}
	if demo {
		e = a.seed()
	} else if em, pw := os.Getenv("BOOTSTRAP_ADMIN_EMAIL"), os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"); em != "" && pw != "" {
		var count int
		db.QueryRow("SELECT count(*) FROM users WHERE role='admin'").Scan(&count)
		if count == 0 {
			if len(pw) < 12 {
				return nil, errors.New("BOOTSTRAP_ADMIN_PASSWORD must have at least 12 characters")
			}
			_, e = a.createUser(db, em, pw, "管理员", "admin", 0)
		}
	}
	return a, e
}
func userFromRow(row interface{ Scan(...any) error }) (M, error) {
	var id, bal int64
	var email, name, role, status, created string
	e := row.Scan(&id, &email, &name, &role, &status, &bal, &created)
	return M{"id": id, "email": email, "name": name, "role": role, "status": status, "balance": bal, "created_at": created}, e
}

const userColumns = "id,email,name,role,status,balance,created_at"

func getUser(q queryer, id int64) (M, error) {
	return userFromRow(q.QueryRow("SELECT "+userColumns+" FROM users WHERE id=?", id))
}
func listUsers(q queryer) ([]M, error) {
	rows, e := q.Query("SELECT " + userColumns + " FROM users ORDER BY id DESC")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		m, e := userFromRow(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (a *App) createUser(q queryer, email, password, name, role string, inviter int64) (M, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	r, e := q.Exec("INSERT INTO users(email,name,password,role,created_at,subscription_token,invite_code,inviter_id) VALUES(?,?,?,?,?,?,?,?)", email, name, passwordHash(password), role, now(), randomToken(32), randomToken(7), inviter)
	if e != nil {
		return nil, e
	}
	id, _ := r.LastInsertId()
	return getUser(q, id)
}
func put(q queryer, kind string, uid int64, m M) (M, error) {
	if kind == "subscriptions" {
		if e := prepareSubscription(q, uid, m); e != nil {
			return nil, e
		}
	}
	if kind == "traffic" && text(m, "source") == "" {
		m["source"] = "demo"
	}
	m["user_id"] = uid
	if uid == 0 {
		delete(m, "user_id")
	}
	raw, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	if integer(m, "id") == 0 {
		r, e := q.Exec("INSERT INTO entities(kind,user_id,data)VALUES(?,?,?)", kind, uid, string(raw))
		if e != nil {
			return nil, e
		}
		id, _ := r.LastInsertId()
		m["id"] = id
		raw, _ = json.Marshal(m)
		_, e = q.Exec("UPDATE entities SET data=? WHERE id=?", string(raw), id)
		return m, e
	}
	_, e = q.Exec("UPDATE entities SET data=?,user_id=? WHERE kind=? AND id=?", string(raw), uid, kind, integer(m, "id"))
	return m, e
}
func item(q queryer, kind string, id int64) (M, error) {
	var raw string
	e := q.QueryRow("SELECT data FROM entities WHERE kind=? AND id=?", kind, id).Scan(&raw)
	if e != nil {
		return nil, e
	}
	m := M{}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	e = d.Decode(&m)
	return m, e
}
func list(q queryer, kind string, uid int64) ([]M, error) {
	query := "SELECT data FROM entities WHERE kind=?"
	args := []any{kind}
	if uid > 0 {
		query += " AND user_id=?"
		args = append(args, uid)
	}
	query += " ORDER BY id DESC"
	rows, e := q.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		m := M{}
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if e = d.Decode(&m); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (a *App) transaction(fn func(*sql.Tx) error) error {
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func audit(q queryer, actor M, action, target string) error {
	_, e := put(q, "audit", 0, M{"actor": text(actor, "email"), "action": action, "target": target, "created_at": now()})
	return e
}
func ledger(q queryer, u M, amount int64, kind, desc string) error {
	_, e := put(q, "ledger", integer(u, "id"), M{"user_email": text(u, "email"), "amount": amount, "balance_after": integer(u, "balance"), "kind": kind, "description": desc, "created_at": now()})
	return e
}
func (a *App) current(r *http.Request) (M, error) {
	c, e := r.Cookie("nicecloud_session")
	if e != nil {
		return nil, apiError{401, "请先登录"}
	}
	var id int64
	e = a.db.QueryRow("SELECT user_id FROM sessions WHERE token=? AND expires_at>?", digest(c.Value), now()).Scan(&id)
	if e != nil {
		return nil, apiError{401, "会话已过期，请重新登录"}
	}
	u, e := getUser(a.db, id)
	if e != nil || text(u, "status") != "active" {
		return nil, apiError{401, "账户已停用"}
	}
	return u, nil
}
func (a *App) session(w http.ResponseWriter, r *http.Request, u M) error {
	token := randomToken(32)
	_, e := a.db.Exec("INSERT INTO sessions(token,user_id,expires_at)VALUES(?,?,?)", digest(token), integer(u, "id"), time.Now().UTC().Add(7*24*time.Hour).Format(time.RFC3339))
	if e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "nicecloud_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil || os.Getenv("COOKIE_SECURE") == "1", SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600})
	return nil
}
func body(r *http.Request) (M, error) {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.UseNumber()
	m := M{}
	if e := d.Decode(&m); e != nil {
		return nil, bad("请求 JSON 无效")
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return nil, bad("请求 JSON 无效")
	}
	return m, nil
}
func respond(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if e != nil {
		code := 500
		msg := "服务器处理失败"
		var ae apiError
		if errors.As(e, &ae) {
			code = ae.code
			msg = ae.msg
		} else if errors.Is(e, sql.ErrNoRows) {
			code = 404
			msg = "记录不存在"
		} else {
			log.Printf("API error: %v", e)
		}
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(M{"error": msg})
		return
	}
	if v == nil {
		v = M{"ok": true}
	}
	encoded, err := json.Marshal(M{"data": v})
	if err != nil {
		log.Printf("JSON response error: %v", err)
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"响应数据无效"}`))
		return
	}
	w.Write(encoded)
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				o, e := url.Parse(origin)
				if e != nil || o.Host != r.Host && !a.origins[origin] {
					respond(w, nil, apiError{403, "请求来源不受信任"})
					return
				}
			}
		}
		v, e := a.route(w, r)
		respond(w, v, e)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/subscribe/") {
		a.subscriptionOutput(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Clean(r.URL.Path)
	full := filepath.Join(a.web, path)
	if st, e := os.Stat(full); e == nil && !st.IsDir() {
		http.ServeFile(w, r, full)
		return
	}
	if _, e := os.Stat(filepath.Join(a.web, "index.html")); e == nil {
		http.ServeFile(w, r, filepath.Join(a.web, "index.html"))
		return
	}
	http.Error(w, "前端尚未构建。请在 web 目录运行 npm run build。", 503)
}
func (a *App) allowedAttempt(r *http.Request, email string) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := host + ":" + email
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.attempts[key]
	if time.Now().After(t.until) {
		t = attempt{until: time.Now().Add(10 * time.Minute)}
	}
	t.count++
	a.attempts[key] = t
	if len(a.attempts) > 10000 {
		for k, v := range a.attempts {
			if time.Now().After(v.until) {
				delete(a.attempts, k)
			}
		}
	}
	return t.count <= 10
}
func (a *App) route(w http.ResponseWriter, r *http.Request) (any, error) {
	if e := a.lifecycle(time.Now().UTC()); e != nil {
		return nil, e
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/")
	method := r.Method
	if strings.HasPrefix(p, "node/") {
		return a.nodeRoute(r, strings.TrimPrefix(p, "node/"))
	}
	if p == "health" && method == "GET" {
		return M{"status": "ok", "demo": a.demo}, nil
	}
	if p == "settings/public" && method == "GET" {
		s, e := a.settings()
		if e != nil {
			return nil, e
		}
		return M{"site_name": s["site_name"], "site_description": s["site_description"], "contact_email": s["contact_email"], "demo": a.demo}, nil
	}
	if p == "auth/login" && method == "POST" {
		m, e := body(r)
		if e != nil {
			return nil, e
		}
		email := strings.ToLower(strings.TrimSpace(text(m, "email")))
		if !a.allowedAttempt(r, email) {
			return nil, apiError{429, "登录尝试过多，请稍后重试"}
		}
		var id int64
		var hash, status string
		e = a.db.QueryRow("SELECT id,password,status FROM users WHERE email=?", email).Scan(&id, &hash, &status)
		if e != nil || !passwordOK(text(m, "password"), hash) {
			return nil, apiError{401, "邮箱或密码错误"}
		}
		if status != "active" {
			return nil, apiError{403, "账户已停用"}
		}
		u, e := getUser(a.db, id)
		if e != nil {
			return nil, e
		}
		return u, a.session(w, r, u)
	}
	if p == "auth/register" && method == "POST" {
		settings, e := a.settings()
		if e != nil {
			return nil, e
		}
		if settings["registration_enabled"] == "false" {
			return nil, apiError{403, "当前已关闭注册"}
		}
		m, e := body(r)
		if e != nil {
			return nil, e
		}
		email := strings.ToLower(strings.TrimSpace(text(m, "email")))
		pass := text(m, "password")
		name := strings.TrimSpace(text(m, "name"))
		if !strings.Contains(email, "@") || len(email) > 254 || len(pass) < 10 || len(pass) > 256 || name == "" || len(name) > 80 {
			return nil, bad("请填写有效邮箱、姓名和至少 10 位密码")
		}
		if !a.allowedAttempt(r, email) {
			return nil, apiError{429, "请求过于频繁"}
		}
		var u M
		e = a.transaction(func(tx *sql.Tx) error {
			var inviter int64
			if code := text(m, "invite_code"); code != "" {
				if e := tx.QueryRow("SELECT id FROM users WHERE invite_code=?", code).Scan(&inviter); e != nil {
					return bad("邀请码无效")
				}
			}
			u, e = a.createUser(tx, email, pass, name, "user", inviter)
			if e != nil {
				return apiError{409, "邮箱已注册"}
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
		return u, a.session(w, r, u)
	}
	if p == "auth/logout" && method == "POST" {
		if c, e := r.Cookie("nicecloud_session"); e == nil {
			a.db.Exec("DELETE FROM sessions WHERE token=?", digest(c.Value))
		}
		http.SetCookie(w, &http.Cookie{Name: "nicecloud_session", Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || os.Getenv("COOKIE_SECURE") == "1"})
		return nil, nil
	}
	if (p == "plans" || p == "articles" || p == "notices") && method == "GET" {
		kind := p
		if p == "notices" {
			kind = "articles"
		}
		items, e := list(a.db, kind, 0)
		if e != nil {
			return nil, e
		}
		out := []M{}
		for _, m := range items {
			if kind == "plans" && boolean(m, "active") || kind == "articles" && boolean(m, "published") && (p != "notices" || text(m, "kind") != "doc") {
				out = append(out, m)
			}
		}
		sortDisplay(out)
		return out, nil
	}
	u, e := a.current(r)
	if e != nil {
		return nil, e
	}
	uid := integer(u, "id")
	admin := text(u, "role") == "admin"
	if strings.HasPrefix(p, "admin/") {
		if !admin {
			return nil, apiError{403, "需要管理员权限"}
		}
		return a.admin(r, u, strings.TrimPrefix(p, "admin/"))
	}
	switch p {
	case "auth/me":
		if method == "GET" {
			return u, nil
		}
	case "auth/password":
		if method == "POST" {
			m, e := body(r)
			if e != nil {
				return nil, e
			}
			var hash string
			a.db.QueryRow("SELECT password FROM users WHERE id=?", uid).Scan(&hash)
			if !passwordOK(text(m, "current_password"), hash) {
				return nil, bad("当前密码错误")
			}
			pass := text(m, "new_password")
			if len(pass) < 10 || len(pass) > 256 {
				return nil, bad("新密码需要 10 至 256 位")
			}
			e = a.transaction(func(tx *sql.Tx) error {
				if _, e := tx.Exec("UPDATE users SET password=? WHERE id=?", passwordHash(pass), uid); e != nil {
					return e
				}
				_, e := tx.Exec("DELETE FROM sessions WHERE user_id=?", uid)
				return e
			})
			if e != nil {
				return nil, e
			}
			return nil, a.session(w, r, u)
		}
	case "dashboard":
		if method == "GET" {
			sub, e := a.subscription(a.db, uid)
			if e != nil {
				return nil, e
			}
			articles, e := list(a.db, "articles", 0)
			if e != nil {
				return nil, e
			}
			ann := []M{}
			for _, m := range articles {
				if boolean(m, "published") && text(m, "kind") != "doc" {
					ann = append(ann, m)
				}
			}
			traffic, e := list(a.db, "traffic", uid)
			return M{"user": u, "subscription": sub, "announcements": ann, "traffic": traffic, "subscription_url": a.subURL(r, uid)}, e
		}
	case "nodes":
		if method == "GET" {
			all, e := list(a.db, "nodes", 0)
			out := []M{}
			for _, m := range all {
				if boolean(m, "active") {
					safe := M{}
					for _, key := range []string{"id", "name", "region", "flag", "protocol", "status", "multiplier", "tags", "active", "sort_order", "updated_at"} {
						safe[key] = m[key]
					}
					if boolean(m, "managed") {
						safe["managed"] = true
						_, ready, err := a.managedSubscriptionNode(integer(m, "id"), uid)
						if err != nil {
							return nil, err
						}
						if ready {
							safe["status"] = "online"
						} else {
							safe["status"] = "not_connected"
						}
					}
					out = append(out, safe)
				}
			}
			sortDisplay(out)
			return out, e
		}
	case "orders":
		if method == "GET" {
			return list(a.db, "orders", uid)
		}
		if method == "POST" {
			return a.createOrder(r, u)
		}
	case "subscription":
		if method == "GET" {
			s, e := a.subscription(a.db, uid)
			return M{"subscription": s, "url": a.subURL(r, uid)}, e
		}
	case "subscription/reset":
		if method == "POST" {
			token := randomToken(32)
			e = a.transaction(func(tx *sql.Tx) error {
				if _, e := tx.Exec("UPDATE users SET subscription_token=? WHERE id=?", token, uid); e != nil {
					return e
				}
				return audit(tx, u, "subscription.reset", strconv.FormatInt(uid, 10))
			})
			return M{"url": a.baseURL(r) + "/subscribe/" + token}, e
		}
	case "traffic":
		if method == "GET" {
			return list(a.db, "traffic", uid)
		}
	case "invites":
		if method == "GET" {
			return a.invites(r, u)
		}
	case "invites/code":
		if method == "POST" {
			var code string
			e := a.db.QueryRow("SELECT invite_code FROM users WHERE id=?", uid).Scan(&code)
			return M{"code": code, "invite_url": a.baseURL(r) + "/register?invite=" + code}, e
		}
	case "withdrawals":
		if method == "POST" {
			return a.withdraw(r, u)
		}
	case "tickets":
		if method == "GET" {
			return list(a.db, "tickets", uid)
		}
		if method == "POST" {
			return a.createTicket(r, u)
		}
	}
	parts := strings.Split(p, "/")
	if len(parts) >= 2 {
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		if id <= 0 {
			return nil, bad("记录 ID 无效")
		}
		if parts[0] == "orders" && len(parts) == 3 && method == "POST" {
			if parts[2] == "pay" {
				return a.pay(u, id)
			}
			if parts[2] == "cancel" {
				return a.cancel(u, id)
			}
		}
		if parts[0] == "tickets" {
			return a.ticket(r, u, id, parts)
		}
	}
	return nil, apiError{404, "接口不存在"}
}
func (a *App) settings() (map[string]string, error) {
	rows, e := a.db.Query("SELECT key,value FROM settings")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if e = rows.Scan(&k, &v); e != nil {
			return nil, e
		}
		out[k] = v
	}
	return out, rows.Err()
}
func (a *App) baseURL(r *http.Request) string {
	if s := os.Getenv("PUBLIC_URL"); s != "" {
		return strings.TrimRight(s, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
func (a *App) subURL(r *http.Request, uid int64) string {
	var token string
	if e := a.db.QueryRow("SELECT subscription_token FROM users WHERE id=?", uid).Scan(&token); e != nil {
		return ""
	}
	return a.baseURL(r) + "/subscribe/" + token
}
func (a *App) subscription(q queryer, uid int64) (M, error) {
	all, e := list(q, "subscriptions", uid)
	if e != nil {
		return nil, e
	}
	if len(all) == 0 {
		return nil, nil
	}
	s := all[0]
	if e := prepareSubscription(q, uid, s); e != nil {
		return nil, e
	}
	if exp, e := time.Parse(time.RFC3339, text(s, "expires_at")); e == nil && exp.Before(time.Now()) && text(s, "status") == "active" {
		s["status"] = "expired"
	}
	return s, nil
}
func (a *App) createOrder(r *http.Request, u M) (any, error) {
	m, e := body(r)
	if e != nil {
		return nil, e
	}
	var out M
	e = a.transaction(func(tx *sql.Tx) error {
		p, e := item(tx, "plans", integer(m, "plan_id"))
		if e != nil || !boolean(p, "active") {
			return bad("套餐不存在或已下架")
		}
		period := text(m, "period")
		keys := map[string]string{"monthly": "monthly_price", "quarterly": "quarterly_price", "yearly": "yearly_price", "onetime": "onetime_price"}
		key := keys[period]
		if key == "" || text(p, "kind") == "onetime" && period != "onetime" || text(p, "kind") != "onetime" && period == "onetime" {
			return bad("计费周期无效")
		}
		amount := integer(p, key)
		if amount <= 0 {
			return bad("当前周期不可购买")
		}
		out, e = put(tx, "orders", integer(u, "id"), M{"order_no": "NC" + time.Now().UTC().Format("20060102150405") + strings.ToUpper(randomToken(5)), "user_email": text(u, "email"), "plan_id": integer(p, "id"), "plan_name": text(p, "name"), "period": period, "amount": amount, "status": "pending", "created_at": now(), "plan_snapshot": p})
		return e
	})
	return cleanOrder(out), e
}
func cleanOrder(m M) M {
	if m != nil {
		delete(m, "plan_snapshot")
	}
	return m
}
func (a *App) pay(u M, id int64) (any, error) {
	var out M
	e := a.transaction(func(tx *sql.Tx) error {
		o, e := item(tx, "orders", id)
		if e != nil {
			return e
		}
		if integer(o, "user_id") != integer(u, "id") {
			return apiError{403, "无权访问此订单"}
		}
		if text(o, "status") == "paid" {
			out = o
			return nil
		}
		if text(o, "status") != "pending" {
			return bad("订单不可支付")
		}
		if created, e := time.Parse(time.RFC3339, text(o, "created_at")); e != nil || time.Since(created) > 30*time.Minute {
			return bad("订单已超时")
		}
		fresh, e := getUser(tx, integer(u, "id"))
		if e != nil {
			return e
		}
		amount := integer(o, "amount")
		if integer(fresh, "balance") < amount {
			return bad("余额不足；第三方支付尚未接入")
		}
		p, ok := o["plan_snapshot"].(map[string]any)
		if !ok {
			return bad("套餐快照不存在")
		}
		s, e := a.subscription(tx, integer(u, "id"))
		if e != nil {
			return e
		}
		start := time.Now().UTC()
		if text(o, "period") != "onetime" && s != nil && integer(s, "plan_id") == integer(o, "plan_id") && (text(s, "status") == "active" || text(s, "status") == "suspended") {
			if ex, e := time.Parse(time.RFC3339, text(s, "expires_at")); e == nil && ex.After(start) {
				start = ex
			}
		}
		expires := start.AddDate(0, 1, 0)
		switch text(o, "period") {
		case "quarterly":
			expires = start.AddDate(0, 3, 0)
		case "yearly":
			expires = start.AddDate(1, 0, 0)
		case "onetime":
			expires = start.AddDate(100, 0, 0)
		}
		if s == nil {
			s = M{}
		}
		suspended := text(s, "status") == "suspended"
		same := text(o, "period") != "onetime" && integer(s, "plan_id") == integer(o, "plan_id") && (text(s, "status") == "active" || suspended)
		used := float64(0)
		if same {
			used = num(s, "traffic_used")
		}
		s["kind"] = text(M(p), "kind")
		s["plan_id"] = integer(o, "plan_id")
		s["user_email"] = text(u, "email")
		s["plan_name"] = text(o, "plan_name")
		s["traffic_limit"] = num(M(p), "traffic_gb")
		s["traffic_used"] = used
		if !same {
			if e = newTrafficCycle(tx, integer(u, "id"), s); e != nil {
				return e
			}
		}
		s["device_limit"] = integer(M(p), "device_limit")
		s["speed_mbps"] = integer(M(p), "speed_mbps")
		s["status"] = "active"
		if suspended {
			s["status"] = "suspended"
		}
		s["expires_at"] = expires.Format(time.RFC3339)
		if text(o, "period") == "onetime" {
			s["reset_at"] = ""
		} else if !same {
			s["reset_at"] = time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339)
		}
		if _, e = put(tx, "subscriptions", integer(u, "id"), s); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE users SET balance=balance-? WHERE id=? AND balance>=?", amount, integer(u, "id"), amount); e != nil {
			return e
		}
		fresh["balance"] = integer(fresh, "balance") - amount
		if e = ledger(tx, fresh, -amount, "purchase", "购买套餐 "+text(o, "plan_name")); e != nil {
			return e
		}
		o["status"] = "paid"
		o["paid_at"] = now()
		out, e = put(tx, "orders", integer(u, "id"), o)
		if e != nil {
			return e
		}
		var inviter int64
		tx.QueryRow("SELECT inviter_id FROM users WHERE id=?", integer(u, "id")).Scan(&inviter)
		if inviter > 0 {
			var rateText string
			tx.QueryRow("SELECT value FROM settings WHERE key='invite_rate'").Scan(&rateText)
			rate, _ := strconv.ParseFloat(rateText, 64)
			if rate >= 0 && rate <= 1 {
				_, e = put(tx, "commissions", inviter, M{"user_email": text(u, "email"), "order_no": text(o, "order_no"), "amount": int64(math.Round(float64(amount) * rate)), "status": "available", "created_at": now()})
			}
		}
		if e != nil {
			return e
		}
		return audit(tx, u, "order.pay", text(o, "order_no"))
	})
	return cleanOrder(out), e
}
func (a *App) cancel(u M, id int64) (any, error) {
	var out M
	e := a.transaction(func(tx *sql.Tx) error {
		o, e := item(tx, "orders", id)
		if e != nil {
			return e
		}
		if integer(o, "user_id") != integer(u, "id") {
			return apiError{403, "无权访问此订单"}
		}
		if text(o, "status") != "pending" {
			return bad("只有待支付订单可以取消")
		}
		o["status"] = "cancelled"
		out, e = put(tx, "orders", integer(u, "id"), o)
		return e
	})
	return cleanOrder(out), e
}
func (a *App) invites(r *http.Request, u M) (any, error) {
	uid := integer(u, "id")
	var code string
	var count int
	if e := a.db.QueryRow("SELECT invite_code FROM users WHERE id=?", uid).Scan(&code); e != nil {
		return nil, e
	}
	a.db.QueryRow("SELECT count(*) FROM users WHERE inviter_id=?", uid).Scan(&count)
	records, e := list(a.db, "commissions", uid)
	if e != nil {
		return nil, e
	}
	withdrawals, e := list(a.db, "withdrawals", uid)
	if e != nil {
		return nil, e
	}
	available, pending := commissionFunds(records, withdrawals)
	s, e := a.settings()
	rate, _ := strconv.ParseFloat(s["invite_rate"], 64)
	if !(rate >= 0 && rate <= 1) {
		rate = 0
	}
	return M{"code": code, "invite_url": a.baseURL(r) + "/register?invite=" + code, "registered": count, "balance": available, "pending": pending, "rate": rate, "records": records, "withdrawals": withdrawals}, e
}
func commissionFunds(records, withdrawals []M) (int64, int64) {
	var total, pending int64
	for _, m := range records {
		if text(m, "status") == "available" {
			total += integer(m, "amount")
		}
	}
	for _, m := range withdrawals {
		if text(m, "status") != "rejected" {
			total -= integer(m, "amount")
		}
		if text(m, "status") == "pending" {
			pending += integer(m, "amount")
		}
	}
	return total, pending
}
func validMoney(m M, k string) bool {
	v := num(m, k)
	return v == math.Trunc(v) && math.Abs(v) <= 100000000000
}
func (a *App) withdraw(r *http.Request, u M) (any, error) {
	m, e := body(r)
	if e != nil {
		return nil, e
	}
	if !validMoney(m, "amount") || integer(m, "amount") <= 0 || strings.TrimSpace(text(m, "account")) == "" || len(text(m, "account")) > 500 {
		return nil, bad("请输入有效的提现金额和收款账户")
	}
	var out M
	e = a.transaction(func(tx *sql.Tx) error {
		records, e := list(tx, "commissions", integer(u, "id"))
		if e != nil {
			return e
		}
		withdrawals, e := list(tx, "withdrawals", integer(u, "id"))
		if e != nil {
			return e
		}
		available, _ := commissionFunds(records, withdrawals)
		if integer(m, "amount") > available {
			return bad("可提现佣金不足")
		}
		out, e = put(tx, "withdrawals", integer(u, "id"), M{"user_email": text(u, "email"), "amount": integer(m, "amount"), "account": text(m, "account"), "status": "pending", "created_at": now()})
		return e
	})
	return out, e
}
func (a *App) createTicket(r *http.Request, u M) (any, error) {
	m, e := body(r)
	if e != nil {
		return nil, e
	}
	subject := strings.TrimSpace(text(m, "subject"))
	message := strings.TrimSpace(text(m, "message"))
	priority := text(m, "priority")
	if subject == "" || len(subject) > 200 || message == "" || len(message) > 20000 {
		return nil, bad("标题或消息长度无效")
	}
	if priority != "low" && priority != "high" {
		priority = "normal"
	}
	return put(a.db, "tickets", integer(u, "id"), M{"user_email": text(u, "email"), "subject": subject, "priority": priority, "status": "open", "created_at": now(), "updated_at": now(), "messages": []M{{"id": 1, "role": text(u, "role"), "message": message, "created_at": now()}}})
}
func (a *App) ticket(r *http.Request, u M, id int64, parts []string) (any, error) {
	var out M
	e := a.transaction(func(tx *sql.Tx) error {
		t, e := item(tx, "tickets", id)
		if e != nil {
			return e
		}
		if integer(t, "user_id") != integer(u, "id") && text(u, "role") != "admin" {
			return apiError{403, "无权访问此工单"}
		}
		if len(parts) == 2 && r.Method == "GET" {
			out = t
			return nil
		}
		if len(parts) != 3 || r.Method != "POST" {
			return apiError{404, "接口不存在"}
		}
		switch parts[2] {
		case "messages":
			if text(t, "status") == "closed" {
				return bad("工单已关闭")
			}
			m, e := body(r)
			if e != nil {
				return e
			}
			msg := strings.TrimSpace(text(m, "message"))
			if msg == "" || len(msg) > 20000 {
				return bad("消息需要 1 至 20000 字符")
			}
			messages, _ := t["messages"].([]any)
			t["messages"] = append(messages, M{"id": len(messages) + 1, "role": text(u, "role"), "message": msg, "created_at": now()})
			if text(u, "role") == "admin" {
				t["status"] = "answered"
			} else {
				t["status"] = "open"
			}
		case "close":
			t["status"] = "closed"
		default:
			return apiError{404, "接口不存在"}
		}
		t["updated_at"] = now()
		out, e = put(tx, "tickets", integer(t, "user_id"), t)
		if e != nil {
			return e
		}
		return audit(tx, u, "ticket."+parts[2], strconv.FormatInt(id, 10))
	})
	return out, e
}
func main() {
	demoFlag := flag.Bool("demo", false, "enable fictional demo data and accounts")
	listen := flag.String("listen", "127.0.0.1:8080", "listen address")
	dbPath := flag.String("db", "data/nicecloud.db", "SQLite database file")
	flag.Parse()
	demo := *demoFlag || os.Getenv("DEMO_MODE") == "1"
	app, e := openApp(*dbPath, demo)
	if e != nil {
		log.Fatal(e)
	}
	defer app.db.Close()
	server := &http.Server{Addr: *listen, Handler: app, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	stop := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() { <-stop; close(done); server.Close() }()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case t := <-ticker.C:
				if e := app.lifecycle(t.UTC()); e != nil {
					log.Printf("lifecycle: %v", e)
				}
			case <-done:
				return
			}
		}
	}()
	log.Printf("NiceCloud API listening on %s (demo=%t)", *listen, demo)
	if e = server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal(e)
	}
}

func sortDisplay(items []M) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := integer(items[i], "sort_order"), integer(items[j], "sort_order")
		if left == right {
			return integer(items[i], "id") < integer(items[j], "id")
		}
		return left < right
	})
}
