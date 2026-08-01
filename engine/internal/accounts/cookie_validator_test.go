package accounts

import (
	"strings"
	"testing"
)

func TestHasLoginCookieMarker(t *testing.T) {
	if !hasLoginCookieMarker("ttwid=value; sessionid_ss=login-token") {
		t.Fatal("expected a Douyin login cookie marker")
	}
	if hasLoginCookieMarker("ttwid=value; csrf_session_id=token") {
		t.Fatal("non-login cookies must not be treated as a valid login state")
	}
}

func TestAccountInfoValid(t *testing.T) {
	valid, message := accountInfoValid(map[string]any{
		"data": map[string]any{
			"account": map[string]any{"screen_name": "测试账号"},
		},
	})
	if !valid || message != "" {
		t.Fatalf("expected valid account info, got valid=%v message=%q", valid, message)
	}

	valid, message = accountInfoValid(map[string]any{
		"data": map[string]any{"description": "会话过期，请重新登录"},
	})
	if valid || !strings.Contains(message, "CK 已失效") {
		t.Fatalf("expected expired account info, got valid=%v message=%q", valid, message)
	}
}

func TestPassportFailureTakesPriorityOverProfileIdentity(t *testing.T) {
	valid, message := accountInfoValid(map[string]any{
		"message": "error",
		"data":    map[string]any{"description": "用户不存在"},
	})
	if valid || !strings.Contains(message, "用户不存在") {
		t.Fatalf("passport failure must mark the browser login as expired, got valid=%v message=%q", valid, message)
	}
}

func TestSelfProfileValid(t *testing.T) {
	valid, message := selfProfileValid(map[string]any{
		"status_code": 0,
		"user":        map[string]any{"nickname": "测试账号"},
	})
	if !valid || message != "" {
		t.Fatalf("expected valid self profile, got valid=%v message=%q", valid, message)
	}

	valid, message = selfProfileValid(map[string]any{
		"status_code": 8,
		"status_msg":  "用户未登录",
	})
	if valid || !strings.Contains(message, "CK 已失效") {
		t.Fatalf("expected expired self profile, got valid=%v message=%q", valid, message)
	}
}
