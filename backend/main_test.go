package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) *App {
	t.Helper()
	a, e := openApp(filepath.Join(t.TempDir(), "test.db"), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}
func request(a *App, method, path string, payload any, cookie *http.Cookie) (*httptest.ResponseRecorder, M) {
	var b bytes.Buffer
	if payload != nil {
		json.NewEncoder(&b).Encode(payload)
	}
	r := httptest.NewRequest(method, path, &b)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	m := M{}
	json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}
func mustRequest(t *testing.T, a *App, method, path string, payload any, cookie *http.Cookie, code int) M {
	t.Helper()
	w, m := request(a, method, path, payload, cookie)
	if w.Code != code {
		t.Fatalf("%s %s: got %d expected %d: %s", method, path, w.Code, code, w.Body.String())
	}
	data, _ := m["data"].(map[string]any)
	return M(data)
}
func login(t *testing.T, a *App, email string) *http.Cookie {
	t.Helper()
	w, _ := request(a, "POST", "/api/auth/login", M{"email": email, "password": "DemoCloud2026!"}, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie")
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("insecure session cookie")
	}
	return cookies[0]
}
func firstPlan(t *testing.T, a *App, kind string) M {
	t.Helper()
	plans, e := list(a.db, "plans", 0)
	if e != nil {
		t.Fatal(e)
	}
	var result M
	for _, p := range plans {
		if text(p, "kind") == kind && (result == nil || integer(p, "sort_order") < integer(result, "sort_order")) {
			result = p
		}
	}
	if result == nil {
		t.Fatal("plan missing")
	}
	return result
}
func order(t *testing.T, a *App, c *http.Cookie, p M, period string) M {
	return mustRequest(t, a, "POST", "/api/orders", M{"plan_id": integer(p, "id"), "period": period}, c, 200)
}
func entityPath(kind string, m M, suffix string) string {
	return fmt.Sprintf("/api/%s/%d%s", kind, integer(m, "id"), suffix)
}

func TestAuthenticationAndOrigin(t *testing.T) {
	a := fixture(t)
	mustRequest(t, a, "GET", "/api/auth/me", nil, nil, 401)
	user := login(t, a, "demo@example.test")
	admin := login(t, a, "admin@example.test")
	mustRequest(t, a, "GET", "/api/admin/users", nil, user, 403)
	mustRequest(t, a, "GET", "/api/admin/stats", nil, admin, 200)
	r := httptest.NewRequest("POST", "/api/subscription/reset", nil)
	r.AddCookie(user)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross origin request accepted: %d", w.Code)
	}
	mustRequest(t, a, "PATCH", "/api/admin/settings", M{"invite_rate": "NaN"}, admin, 400)
	mustRequest(t, a, "PATCH", "/api/admin/settings", M{"registration_enabled": "false"}, admin, 200)
	mustRequest(t, a, "POST", "/api/auth/register", M{"email": "new@example.test", "name": "new", "password": "AValidPassword2026!"}, nil, 403)
	for i := 0; i < 11; i++ {
		w, _ := request(a, "POST", "/api/auth/login", M{"email": "no@example.test", "password": "incorrect"}, nil)
		if i == 10 && w.Code != 429 {
			t.Fatalf("login not rate limited: %d", w.Code)
		}
	}
	mustRequest(t, a, "POST", "/api/auth/logout", nil, user, 200)
	mustRequest(t, a, "GET", "/api/auth/me", nil, user, 401)
}

func TestAtomicPaymentSnapshotCancellationAndOneTime(t *testing.T) {
	a := fixture(t)
	c := login(t, a, "demo@example.test")
	u := mustRequest(t, a, "GET", "/api/auth/me", nil, c, 200)
	p := firstPlan(t, a, "recurring")
	o := order(t, a, c, p, "monthly")
	original := num(p, "traffic_gb")
	p["traffic_gb"] = 99999
	p["monthly_price"] = 99000
	if _, e := put(a.db, "plans", 0, p); e != nil {
		t.Fatal(e)
	}
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, _ := request(a, "POST", entityPath("orders", o, "/pay"), M{}, c)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent pay: %d", code)
		}
	}
	after, _ := getUser(a.db, integer(u, "id"))
	if integer(after, "balance") != integer(u, "balance")-integer(o, "amount") {
		t.Fatalf("duplicate deduction: %v", after)
	}
	s, e := a.subscription(a.db, integer(u, "id"))
	if e != nil || num(s, "traffic_limit") != original {
		t.Fatalf("snapshot not applied: %v %v", s, e)
	}
	entries, _ := list(a.db, "ledger", integer(u, "id"))
	count := 0
	for _, x := range entries {
		if text(x, "kind") == "purchase" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate purchase ledger: %d", count)
	}
	p["monthly_price"] = 100
	put(a.db, "plans", 0, p)
	cancelled := order(t, a, c, p, "monthly")
	mustRequest(t, a, "POST", entityPath("orders", cancelled, "/cancel"), M{}, c, 200)
	mustRequest(t, a, "POST", entityPath("orders", cancelled, "/pay"), M{}, c, 400)
	one := firstPlan(t, a, "onetime")
	once := order(t, a, c, one, "onetime")
	mustRequest(t, a, "POST", entityPath("orders", once, "/pay"), M{}, c, 200)
	s, _ = a.subscription(a.db, integer(u, "id"))
	s["traffic_used"] = num(s, "traffic_limit")
	put(a.db, "subscriptions", integer(u, "id"), s)
	again := order(t, a, c, one, "onetime")
	mustRequest(t, a, "POST", entityPath("orders", again, "/pay"), M{}, c, 200)
	s, _ = a.subscription(a.db, integer(u, "id"))
	if num(s, "traffic_used") != 0 || num(s, "traffic_limit") != num(one, "traffic_gb") {
		t.Fatalf("repeat one-time purchase failed to refresh quota: %v", s)
	}
}

func TestSubscriptionResetNodeSecretsAndOwnership(t *testing.T) {
	a := fixture(t)
	c := login(t, a, "demo@example.test")
	admin := login(t, a, "admin@example.test")
	before := mustRequest(t, a, "GET", "/api/subscription", nil, c, 200)
	oldURL, _ := url.Parse(text(before, "url"))
	w, _ := request(a, "GET", oldURL.RequestURI(), nil, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	after := mustRequest(t, a, "POST", "/api/subscription/reset", M{}, c, 200)
	w, _ = request(a, "GET", oldURL.RequestURI(), nil, nil)
	if w.Code != 404 {
		t.Fatal("old token remains valid")
	}
	newURL, _ := url.Parse(text(after, "url"))
	w, _ = request(a, "GET", newURL.RequestURI()+"?format=json", nil, nil)
	if strings.Contains(w.Body.String(), "user_email") || strings.Contains(w.Body.String(), "user_id") {
		t.Fatal("subscription leaks account identity")
	}
	mustRequest(t, a, "POST", "/api/admin/nodes", M{"name": "Configured VLESS", "host": "node.example.test", "protocol": "vless", "uuid": "secret-uuid", "status": "available"}, admin, 200)
	w, _ = request(a, "GET", "/api/nodes", nil, c)
	for _, secret := range []string{"secret-uuid", "node.example.test", "\"host\"", "\"uuid\""} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("public nodes leaks %s", secret)
		}
	}
	w, _ = request(a, "GET", newURL.RequestURI(), nil, nil)
	if !strings.Contains(w.Body.String(), "secret-uuid") {
		t.Fatal("authorized VLESS missing from subscription")
	}
	ticket := mustRequest(t, a, "POST", "/api/tickets", M{"subject": "Test", "priority": "normal", "message": "Hello"}, c, 200)
	other, e := a.createUser(a.db, "other@example.test", "DemoCloud2026!", "Other", "user", 0)
	if e != nil {
		t.Fatal(e)
	}
	otherCookie := login(t, a, text(other, "email"))
	mustRequest(t, a, "GET", entityPath("tickets", ticket, ""), nil, otherCookie, 403)
	mustRequest(t, a, "POST", entityPath("tickets", ticket, "/messages"), M{"message": "intrude"}, otherCookie, 403)
	mustRequest(t, a, "POST", entityPath("tickets", ticket, "/close"), M{}, otherCookie, 403)
	mustRequest(t, a, "POST", entityPath("tickets", ticket, "/messages"), M{"message": "Staff reply"}, admin, 200)
	mustRequest(t, a, "POST", entityPath("tickets", ticket, "/close"), M{}, c, 200)
	mustRequest(t, a, "POST", entityPath("tickets", ticket, "/messages"), M{"message": "closed"}, c, 400)
	o := order(t, a, c, firstPlan(t, a, "recurring"), "monthly")
	mustRequest(t, a, "POST", entityPath("orders", o, "/pay"), M{}, otherCookie, 403)
}

func TestConcurrentWithdrawalAndLastAdmin(t *testing.T) {
	a := fixture(t)
	c := login(t, a, "demo@example.test")
	u := mustRequest(t, a, "GET", "/api/auth/me", nil, c, 200)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, _ := request(a, "POST", "/api/withdrawals", M{"amount": 1000, "account": "test account"}, c)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code != 400 {
			t.Fatalf("withdraw: %d", code)
		}
	}
	if success != 1 {
		t.Fatalf("oversubscribed commissions: %d", success)
	}
	records, _ := list(a.db, "commissions", integer(u, "id"))
	withdrawals, _ := list(a.db, "withdrawals", integer(u, "id"))
	available, pending := commissionFunds(records, withdrawals)
	if available != 500 || pending != 1000 {
		t.Fatalf("incorrect reserved funds: %d %d", available, pending)
	}
	admin := login(t, a, "admin@example.test")
	ad := mustRequest(t, a, "GET", "/api/auth/me", nil, admin, 200)
	mustRequest(t, a, "PATCH", entityPath("admin/users", ad, ""), M{"status": "disabled"}, admin, 400)
}

func TestLifecycleAndNonDemoSafety(t *testing.T) {
	a := fixture(t)
	var uid int64
	a.db.QueryRow("SELECT id FROM users WHERE email='demo@example.test'").Scan(&uid)
	s, _ := a.subscription(a.db, uid)
	s["traffic_used"] = 50
	s["reset_at"] = time.Now().UTC().AddDate(0, -1, 0).Format(time.RFC3339)
	s["kind"] = "recurring"
	put(a.db, "subscriptions", uid, s)
	o, _ := put(a.db, "orders", uid, M{"status": "pending", "created_at": time.Now().UTC().Add(-31 * time.Minute).Format(time.RFC3339)})
	if e := a.lifecycle(time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	s, _ = a.subscription(a.db, uid)
	if num(s, "traffic_used") != 0 {
		t.Fatal("monthly usage not reset")
	}
	o, _ = item(a.db, "orders", integer(o, "id"))
	if text(o, "status") != "cancelled" {
		t.Fatal("expired order not cancelled")
	}
	s["expires_at"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	put(a.db, "subscriptions", uid, s)
	a.lifecycle(time.Now().UTC())
	s, _ = a.subscription(a.db, uid)
	if text(s, "status") != "expired" {
		t.Fatal("subscription not expired")
	}
	t.Setenv("BOOTSTRAP_ADMIN_EMAIL", "")
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "")
	b, e := openApp(filepath.Join(t.TempDir(), "clean.db"), false)
	if e != nil {
		t.Fatal(e)
	}
	defer b.db.Close()
	users, e := listUsers(b.db)
	if e != nil || len(users) != 0 {
		t.Fatalf("non-demo seeded default users: %v %v", users, e)
	}
	plans, _ := list(b.db, "plans", 0)
	if len(plans) != 0 {
		t.Fatal("non-demo seeded fictional plans")
	}
	var settings string
	if e = b.db.QueryRow("SELECT value FROM settings WHERE key='registration_enabled'").Scan(&settings); e != nil || settings != "true" {
		t.Fatalf("registration default missing %s %v", settings, e)
	}
	e = b.transaction(func(tx *sql.Tx) error {
		_, e := tx.Exec("INSERT INTO settings(key,value)VALUES('rollback-test','true')")
		if e != nil {
			return e
		}
		return fmt.Errorf("rollback")
	})
	if e == nil {
		t.Fatal("transaction unexpectedly committed")
	}
	var count int
	b.db.QueryRow("SELECT count(*) FROM settings WHERE key='rollback-test'").Scan(&count)
	if count != 0 {
		t.Fatal("transaction did not roll back")
	}
}

func TestRegistrationInvitationAndPasswordRotation(t *testing.T) {
	a := fixture(t)
	inviterCookie := login(t, a, "demo@example.test")
	inviter := mustRequest(t, a, "GET", "/api/auth/me", nil, inviterCookie, 200)
	invitation := mustRequest(t, a, "GET", "/api/invites", nil, inviterCookie, 200)
	parsed, e := url.Parse(text(invitation, "invite_url"))
	if e != nil {
		t.Fatal(e)
	}
	if parsed.Path != "/register" || parsed.Fragment != "" || parsed.Query().Get("invite") != text(invitation, "code") {
		t.Fatalf("invitation is not a BrowserRouter registration URL: %s", text(invitation, "invite_url"))
	}

	var oldSession *http.Cookie
	var registered M
	for _, tc := range []struct {
		email, code string
		inviterID   int64
	}{
		{"ordinary-registration@example.test", "", 0},
		{"invited-registration@example.test", text(invitation, "code"), integer(inviter, "id")},
	} {
		payload := M{"email": tc.email, "name": "New member", "password": "DemoCloud2026!"}
		if tc.code != "" {
			payload["invite_code"] = tc.code
		}
		w, result := request(a, "POST", "/api/auth/register", payload, nil)
		if w.Code != 200 {
			t.Fatalf("register %s: %d %s", tc.email, w.Code, w.Body.String())
		}
		data, _ := result["data"].(map[string]any)
		u := M(data)
		if integer(u, "balance") != 0 || text(u, "role") != "user" || text(u, "status") != "active" {
			t.Fatalf("registration granted incorrect defaults: %v", u)
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("registration session missing: %s", w.Body.String())
		}
		mustRequest(t, a, "GET", "/api/auth/me", nil, cookies[0], 200)
		var inviterID int64
		if e := a.db.QueryRow("SELECT inviter_id FROM users WHERE id=?", integer(u, "id")).Scan(&inviterID); e != nil {
			t.Fatal(e)
		}
		if inviterID != tc.inviterID {
			t.Fatalf("inviter association got %d expected %d", inviterID, tc.inviterID)
		}
		if tc.code != "" {
			registered = u
			oldSession = cookies[0]
		}
	}

	secondSession := login(t, a, text(registered, "email"))
	const newPassword = "RotatedCloudPassword2026!"
	mustRequest(t, a, "POST", "/api/auth/password", M{"current_password": "incorrect-password", "new_password": newPassword}, oldSession, 400)
	// An unsuccessful password change leaves both existing sessions valid.
	mustRequest(t, a, "GET", "/api/auth/me", nil, oldSession, 200)
	mustRequest(t, a, "GET", "/api/auth/me", nil, secondSession, 200)
	w, _ := request(a, "POST", "/api/auth/password", M{"current_password": "DemoCloud2026!", "new_password": newPassword}, oldSession)
	if w.Code != 200 {
		t.Fatalf("password rotation failed: %d %s", w.Code, w.Body.String())
	}
	newCookies := w.Result().Cookies()
	if len(newCookies) != 1 {
		t.Fatal("password rotation did not establish a new session")
	}
	mustRequest(t, a, "GET", "/api/auth/me", nil, oldSession, 401)
	mustRequest(t, a, "GET", "/api/auth/me", nil, secondSession, 401)
	current := mustRequest(t, a, "GET", "/api/auth/me", nil, newCookies[0], 200)
	if integer(current, "id") != integer(registered, "id") {
		t.Fatal("new session belongs to a different user")
	}
	mustRequest(t, a, "POST", "/api/auth/login", M{"email": text(registered, "email"), "password": "DemoCloud2026!"}, nil, 401)
	signedIn := mustRequest(t, a, "POST", "/api/auth/login", M{"email": text(registered, "email"), "password": newPassword}, nil, 200)
	if integer(signedIn, "id") != integer(registered, "id") {
		t.Fatal("new password did not authenticate the registered user")
	}
}
