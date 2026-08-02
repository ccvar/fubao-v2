package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ImportSource carries file content only for the duration of an explicit
// import request. It is never persisted outside the permission-restricted
// account store.
type ImportSource struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type CookieImportRecord struct {
	Cookie   string
	Name     string
	Nickname string
	UserID   string
	SecUID   string
	Source   string
}

var (
	copiedCookieHeader = regexp.MustCompile(`(?i)cookie\s*:\s*([^\r\n"']+)`)
	curlCookieArgument = regexp.MustCompile(`(?is)(?:^|\s)(?:-b|--cookie)\s+['"]([^'"]+)['"]`)
)

// ParseImportSources accepts legacy 福宝 JSON, browser Cookie JSON exports,
// raw Cookie text and one-Cookie-per-line text files.
func ParseImportSources(sources []ImportSource) ([]CookieImportRecord, int) {
	records := make([]CookieImportRecord, 0)
	invalid := 0
	for _, source := range sources {
		parsed := parseImportSource(source)
		if len(parsed) == 0 && strings.TrimSpace(source.Content) != "" {
			invalid++
		}
		records = append(records, parsed...)
	}

	deduped := make([]CookieImportRecord, 0, len(records))
	seen := map[string]bool{}
	for _, record := range records {
		key := importedCookieKey(record.Cookie)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, record)
	}
	return deduped, invalid
}

func parseImportSource(source ImportSource) []CookieImportRecord {
	content := strings.TrimSpace(source.Content)
	if content == "" {
		return nil
	}
	var payload any
	if json.Unmarshal([]byte(content), &payload) == nil {
		if records := recordsFromJSON(payload, source.Name); len(records) > 0 {
			return records
		}
	}
	if cookie := netscapeCookieText(content); cookie != "" {
		return []CookieImportRecord{newImportRecord(source.Name, 1, cookie)}
	}
	if match := curlCookieArgument.FindStringSubmatch(content); len(match) == 2 {
		if cookie := normalizeImportedCookie(match[1]); cookie != "" {
			return []CookieImportRecord{newImportRecord(source.Name, 1, cookie)}
		}
	}

	if match := copiedCookieHeader.FindStringSubmatch(content); len(match) == 2 {
		if cookie := normalizeImportedCookie(match[1]); cookie != "" {
			return []CookieImportRecord{newImportRecord(source.Name, 1, cookie)}
		}
	}

	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	records := make([]CookieImportRecord, 0, len(lines))
	for index, line := range lines {
		if cookie := normalizeImportedCookie(line); cookie != "" {
			records = append(records, newImportRecord(source.Name, index+1, cookie))
		}
	}
	if len(records) == 0 {
		if cookie := normalizeImportedCookie(content); cookie != "" {
			return []CookieImportRecord{newImportRecord(source.Name, 1, cookie)}
		}
	}
	return records
}

func recordsFromJSON(value any, sourceName string) []CookieImportRecord {
	switch item := value.(type) {
	case []any:
		if cookie := browserCookieObjects(item); cookie != "" {
			return []CookieImportRecord{newImportRecord(sourceName, 1, cookie)}
		}
		var records []CookieImportRecord
		for index, child := range item {
			records = append(records, recordsFromJSONItem(child, sourceName, index+1)...)
		}
		return records
	case map[string]any:
		for _, key := range []string{"cookies", "cookieStore", "cookie_store"} {
			if values, ok := item[key].([]any); ok {
				if cookie := browserCookieObjects(values); cookie != "" {
					return []CookieImportRecord{recordFromMap(item, sourceName, 1, cookie)}
				}
			}
		}
		var records []CookieImportRecord
		for _, key := range []string{"data", "accounts", "list", "items"} {
			if values, ok := item[key].([]any); ok {
				for index, child := range values {
					records = append(records, recordsFromJSONItem(child, sourceName, index+1)...)
				}
			}
		}
		if len(records) > 0 {
			return records
		}
		if cookie := mapString(item, "CK", "Cookie", "cookie", "cookies"); normalizeImportedCookie(cookie) != "" {
			return []CookieImportRecord{recordFromMap(item, sourceName, 1, cookie)}
		}
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if child := recordsFromJSON(item[key], sourceName); len(child) > 0 {
				return child
			}
		}
	}
	return nil
}

func recordsFromJSONItem(value any, sourceName string, index int) []CookieImportRecord {
	if item, ok := value.(map[string]any); ok {
		if cookie := mapString(item, "CK", "Cookie", "cookie", "cookies"); normalizeImportedCookie(cookie) != "" {
			return []CookieImportRecord{recordFromMap(item, sourceName, index, cookie)}
		}
	}
	if text, ok := value.(string); ok {
		if cookie := normalizeImportedCookie(text); cookie != "" {
			return []CookieImportRecord{newImportRecord(sourceName, index, cookie)}
		}
	}
	return recordsFromJSON(value, sourceName)
}

func browserCookieObjects(values []any) string {
	pairs := make([]string, 0, len(values))
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		domain := mapString(item, "domain", "host")
		if domain != "" && !isDouyinCookieDomain(domain) {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(item["name"]))
		cookieValue := strings.TrimSpace(fmt.Sprint(item["value"]))
		if name == "" || cookieValue == "" || name == "<nil>" || cookieValue == "<nil>" {
			continue
		}
		pairs = append(pairs, name+"="+cookieValue)
	}
	return normalizeImportedCookie(strings.Join(pairs, "; "))
}

func netscapeCookieText(content string) string {
	pairs := make([]string, 0)
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		columns := strings.Split(line, "\t")
		if len(columns) < 7 || !isDouyinCookieDomain(columns[0]) {
			continue
		}
		name := strings.TrimSpace(columns[5])
		value := strings.TrimSpace(columns[6])
		if name != "" && value != "" {
			pairs = append(pairs, name+"="+value)
		}
	}
	return normalizeImportedCookie(strings.Join(pairs, "; "))
}

func isDouyinCookieDomain(domain string) bool {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	for _, allowed := range []string{"douyin.com", "iesdouyin.com", "amemv.com", "bytedance.com"} {
		if domain == allowed || strings.HasSuffix(domain, "."+allowed) {
			return true
		}
	}
	return false
}

func recordFromMap(item map[string]any, sourceName string, index int, cookie string) CookieImportRecord {
	record := newImportRecord(sourceName, index, normalizeImportedCookie(cookie))
	record.Name = firstNonEmpty(mapString(item, "name", "account_name", "display_name"), record.Name)
	record.Nickname = firstNonEmpty(mapString(item, "nickname", "nick_name"), record.Name)
	record.UserID = mapString(item, "user_id", "userId", "uid", "douyin_id")
	record.SecUID = mapString(item, "sec_uid", "secUid", "sec_user_id")
	return record
}

func newImportRecord(sourceName string, index int, cookie string) CookieImportRecord {
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(sourceName)), filepath.Ext(sourceName))
	if base == "" || base == "." {
		base = "导入账号"
	}
	name := base
	if index > 1 {
		name = fmt.Sprintf("%s %d", base, index)
	}
	return CookieImportRecord{Cookie: cookie, Name: name, Nickname: name, Source: firstNonEmpty(sourceName, "manual")}
}

func normalizeImportedCookie(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`'\"")
	if strings.HasPrefix(strings.ToLower(value), "cookie:") {
		value = strings.TrimSpace(value[len("cookie:"):])
	}
	if !strings.Contains(value, "=") || !hasLoginCookieMarker(value) {
		return ""
	}
	parts := make([]string, 0)
	for _, raw := range strings.Split(value, ";") {
		name, cookieValue, ok := strings.Cut(strings.TrimSpace(raw), "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(cookieValue) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(name)+"="+strings.TrimSpace(cookieValue))
	}
	return strings.Join(parts, "; ")
}

func mapString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func importedCookieKey(cookie string) string {
	if session := cookieValue(cookie, "sessionid_ss", "sessionid", "sid_guard"); session != "" {
		sum := sha256.Sum256([]byte(session))
		return "session:" + hex.EncodeToString(sum[:])
	}
	return ""
}
