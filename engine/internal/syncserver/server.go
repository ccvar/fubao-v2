package syncserver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"fubao.ccvar.com/engine/internal/syncprotocol"
)

const maxRequestBody = 2 << 20

type Server struct {
	store           *Store
	enrollmentToken string
	version         string
}

func New(store *Store, enrollmentToken, version string) (*Server, error) {
	if store == nil {
		return nil, errors.New("同步数据库未初始化")
	}
	enrollmentToken = strings.TrimSpace(enrollmentToken)
	if len(enrollmentToken) < 32 {
		return nil, errors.New("设备注册令牌至少需要 32 个字符")
	}
	return &Server{store: store, enrollmentToken: enrollmentToken, version: version}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/healthz", s.health)
	mux.HandleFunc("POST /api/v1/devices/register", s.register)
	mux.HandleFunc("POST /api/v1/sync/batch", s.syncBatch)
	return securityHeaders(mux)
}

func (s *Server) health(writer http.ResponseWriter, request *http.Request) {
	stats, err := s.store.Stats(request.Context())
	if err != nil {
		writeJSONError(writer, http.StatusServiceUnavailable, "database_unavailable", "同步数据库暂不可用")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "service": "fubao-sync-server", "version": s.version, "time": time.Now().UTC().Format(time.RFC3339Nano), "stats": stats,
	})
}

func (s *Server) register(writer http.ResponseWriter, request *http.Request) {
	if !secureEqual(bearerToken(request), s.enrollmentToken) {
		writeJSONError(writer, http.StatusUnauthorized, "invalid_enrollment_token", "设备注册令牌无效")
		return
	}
	var input syncprotocol.RegisterRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeJSONError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if input.Version != syncprotocol.Version {
		writeJSONError(writer, http.StatusBadRequest, "protocol_mismatch", "同步协议版本不兼容")
		return
	}
	result, err := s.store.RegisterDevice(request.Context(), input)
	if err != nil {
		writeJSONError(writer, http.StatusBadRequest, "registration_failed", err.Error())
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (s *Server) syncBatch(writer http.ResponseWriter, request *http.Request) {
	token := bearerToken(request)
	if token == "" {
		writeJSONError(writer, http.StatusUnauthorized, "missing_device_token", "缺少同步设备令牌")
		return
	}
	clientID, err := s.store.AuthorizeDevice(request.Context(), token)
	if err != nil {
		writeJSONError(writer, http.StatusUnauthorized, "invalid_device_token", "同步设备令牌无效")
		return
	}
	var input syncprotocol.BatchRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeJSONError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	result, err := s.store.ApplyBatch(request.Context(), clientID, input)
	if err != nil {
		writeJSONError(writer, http.StatusBadRequest, "sync_failed", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("请求内容为空")
		}
		return errors.New("请求 JSON 无效")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func bearerToken(request *http.Request) string {
	value := strings.TrimSpace(request.Header.Get("Authorization"))
	if len(value) < 8 || !strings.EqualFold(value[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}

func secureEqual(left, right string) bool {
	if len(left) != len(right) || left == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'none'")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeJSONError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
