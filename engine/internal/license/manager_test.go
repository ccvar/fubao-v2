package license

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultStatusIsFree(t *testing.T) {
	m, err := New(t.TempDir(), "0.1.0", Config{})
	if err != nil {
		t.Fatal(err)
	}
	status := m.Status()
	if status.Edition != "免费版" || status.State != "inactive" {
		t.Fatalf("unexpected default status: %+v", status)
	}
}

func TestActivatePersistsProfessionalStatus(t *testing.T) {
	var validations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch r.URL.Path {
		case "/test/licenses/actions/validate-key":
			valid := validations.Add(1) > 1
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"valid": valid, "detail": ""},
				"data": map[string]any{"id": "license-1", "attributes": map[string]any{"metadata": map[string]any{"plan": "专业版"}}},
			})
		case "/test/machines":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": "machine-1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	m, err := New(dir, "0.1.0", Config{Account: "test", Product: "product-1", APIBase: server.URL, HTTPClient: server.Client(), OfflineGraceDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC) }
	result := m.Activate(context.Background(), "TEST-LICENSE-KEY-1234", "Test Mac")
	if !result.Success || result.Status.Edition != "专业版" {
		t.Fatalf("activation failed: %+v", result)
	}
	if result.Status.LicenseKeyMasked != "TEST...1234" {
		t.Fatalf("license key was not safely masked: %q", result.Status.LicenseKeyMasked)
	}

	reloaded, err := New(dir, "0.1.0", Config{Account: "test", Product: "product-1", APIBase: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = m.now
	if got := reloaded.Status(); got.Edition != "专业版" || got.State != "active" {
		t.Fatalf("persisted status mismatch: %+v", got)
	}
	info, err := os.Stat(filepath.Join(dir, "license_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("license state permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestActivateFallsBackToProductValidationForLegacyPolicy(t *testing.T) {
	var validations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch r.URL.Path {
		case "/test/machines":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{
				"detail": "License key authentication is not allowed by policy",
				"code":   "LICENSE_NOT_ALLOWED",
			}}})
		case "/test/licenses/actions/validate-key":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			meta, _ := payload["meta"].(map[string]any)
			scope, _ := meta["scope"].(map[string]any)
			call := validations.Add(1)
			valid := call >= 3 && scope["fingerprint"] == nil
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"valid": valid, "code": map[bool]string{true: "VALID", false: "NO_MACHINES"}[valid]},
				"data": map[string]any{"id": "license-legacy", "attributes": map[string]any{"metadata": map[string]any{"plan": "专业版"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	m, err := New(t.TempDir(), "0.1.0", Config{Account: "test", Product: "product-1", APIBase: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	result := m.Activate(context.Background(), "LEGACY-LICENSE-KEY-1234", "Legacy Mac")
	if !result.Success || result.Status.Edition != "专业版" {
		t.Fatalf("legacy policy fallback failed: %+v", result)
	}
	if m.state.MachineID != "" {
		t.Fatalf("product-only fallback must not invent a machine id: %q", m.state.MachineID)
	}
}
