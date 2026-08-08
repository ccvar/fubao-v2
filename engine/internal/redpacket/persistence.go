package redpacket

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

type persistedStoreCandidate struct {
	path  string
	label string
	mtime time.Time
}

// loadPersistedStoreFile keeps a damaged Windows write from disabling every
// red-packet-backed page. The canonical file remains the first choice; only a
// malformed/missing canonical file permits a committed temp snapshot or the
// previous valid backup to take over.
func loadPersistedStoreFile(path string) (file, bool, error) {
	candidates, err := persistedStoreCandidates(path)
	if err != nil {
		return file{}, false, fmt.Errorf("读取红包监测数据失败: %w", err)
	}
	if len(candidates) == 0 {
		return file{}, false, nil
	}

	var firstParseErr error
	for _, candidate := range candidates {
		raw, readErr := os.ReadFile(candidate.path)
		if readErr != nil {
			if candidate.path == path {
				return file{}, false, fmt.Errorf("读取红包监测数据失败: %w", readErr)
			}
			continue
		}
		normalized, repaired, normalizeErr := normalizePersistedJSON(raw)
		if normalizeErr != nil {
			if firstParseErr == nil {
				firstParseErr = normalizeErr
			}
			continue
		}
		var payload file
		if unmarshalErr := json.Unmarshal(normalized, &payload); unmarshalErr != nil {
			if firstParseErr == nil {
				firstParseErr = unmarshalErr
			}
			continue
		}

		if candidate.path != path || repaired {
			if writeErr := writePersistedStoreFile(path, normalized); writeErr != nil {
				return file{}, false, fmt.Errorf("恢复红包监测数据失败: %w", writeErr)
			}
			fmt.Fprintf(os.Stderr, "红包监测数据已从%s恢复\n", candidate.label)
		}
		return payload, true, nil
	}

	// No bytes can be trusted. Preserve the damaged canonical file under a
	// timestamped name and start with a valid empty payload so the room and
	// participation tabs remain usable. Nothing is silently deleted.
	emptyPayload := file{Version: storeVersion}
	encoded, marshalErr := json.Marshal(emptyPayload)
	if marshalErr != nil {
		return file{}, false, fmt.Errorf("创建空红包监测数据失败: %w", marshalErr)
	}
	if writeErr := writePersistedStoreFile(path, encoded); writeErr != nil {
		if firstParseErr == nil {
			firstParseErr = errors.New("未找到可恢复的 JSON 快照")
		}
		return file{}, false, fmt.Errorf("解析红包监测数据失败: %v；重建失败: %w", firstParseErr, writeErr)
	}
	if firstParseErr == nil {
		firstParseErr = errors.New("未找到可恢复的 JSON 快照")
	}
	fmt.Fprintf(os.Stderr, "红包监测数据无法解析，已隔离损坏文件并重建空存储: %v\n", firstParseErr)
	return emptyPayload, true, nil
}

func persistedStoreCandidates(path string) ([]persistedStoreCandidate, error) {
	items := make([]persistedStoreCandidate, 0, 4)
	fallbacks := make([]persistedStoreCandidate, 0, 3)
	appendCandidate := func(target *[]persistedStoreCandidate, candidatePath, label string) error {
		info, err := os.Stat(candidatePath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			if candidatePath == path {
				return err
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		*target = append(*target, persistedStoreCandidate{path: candidatePath, label: label, mtime: info.ModTime()})
		return nil
	}

	if err := appendCandidate(&items, path, "主文件可恢复内容"); err != nil {
		return nil, err
	}
	// v0.1.44 and earlier used one fixed temp name. Keep it readable after an
	// interrupted rename, then include the unique temp files used by new builds.
	if err := appendCandidate(&fallbacks, path+".tmp", "未完成的临时快照"); err != nil {
		return nil, err
	}
	tempMatches, _ := filepath.Glob(path + ".tmp-*")
	temps := make([]persistedStoreCandidate, 0, len(tempMatches))
	for _, match := range tempMatches {
		info, statErr := os.Stat(match)
		if statErr == nil && !info.IsDir() {
			temps = append(temps, persistedStoreCandidate{path: match, label: "未完成的临时快照", mtime: info.ModTime()})
		}
	}
	sort.Slice(temps, func(i, j int) bool { return temps[i].mtime.After(temps[j].mtime) })
	fallbacks = append(fallbacks, temps...)
	if err := appendCandidate(&fallbacks, path+".bak", "上一份有效备份"); err != nil {
		return nil, err
	}
	// If both a legacy temp file and a backup survived, use the newest valid
	// committed snapshot instead of blindly preferring a stale temp file.
	sort.SliceStable(fallbacks, func(i, j int) bool { return fallbacks[i].mtime.After(fallbacks[j].mtime) })
	items = append(items, fallbacks...)
	return items, nil
}

// normalizePersistedJSON accepts the damage patterns seen on Windows without
// guessing at business fields: UTF-8 BOM, UTF-16 text, and NUL bytes introduced
// around or between otherwise valid JSON bytes. A candidate is accepted only
// when the repaired result is syntactically complete JSON.
func normalizePersistedJSON(raw []byte) ([]byte, bool, error) {
	try := func(candidate []byte, repaired bool) ([]byte, bool, bool) {
		candidate = bytes.TrimSpace(candidate)
		if json.Valid(candidate) {
			return append([]byte(nil), candidate...), repaired, true
		}
		return nil, false, false
	}
	if normalized, repaired, ok := try(raw, false); ok {
		return normalized, repaired, nil
	}
	if withoutBOM := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}); len(withoutBOM) != len(raw) {
		if normalized, repaired, ok := try(withoutBOM, true); ok {
			return normalized, repaired, nil
		}
	}
	// Apply the common repairs together as well. A write interrupted around an
	// encoded file can contain both leading NUL padding and a UTF-8 BOM.
	combined := bytes.Trim(raw, "\x00 \t\r\n")
	combined = bytes.TrimPrefix(combined, []byte{0xEF, 0xBB, 0xBF})
	combined = bytes.ReplaceAll(combined, []byte{0}, nil)
	combined = bytes.TrimPrefix(bytes.TrimSpace(combined), []byte{0xEF, 0xBB, 0xBF})
	if normalized, repaired, ok := try(combined, true); ok {
		return normalized, repaired, nil
	}
	trimmedNUL := bytes.Trim(raw, "\x00 \t\r\n")
	if normalized, repaired, ok := try(trimmedNUL, true); ok {
		return normalized, repaired, nil
	}
	withoutNUL := bytes.ReplaceAll(raw, []byte{0}, nil)
	if normalized, repaired, ok := try(withoutNUL, true); ok {
		return normalized, repaired, nil
	}
	if decoded, ok := decodePersistedUTF16(raw); ok {
		if normalized, repaired, valid := try(decoded, true); valid {
			return normalized, repaired, nil
		}
	}

	var probe any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &probe); err != nil {
		return nil, false, err
	}
	return nil, false, errors.New("红包监测数据不是有效 JSON")
}

func decodePersistedUTF16(raw []byte) ([]byte, bool) {
	if len(raw) < 4 || len(raw)%2 != 0 {
		return nil, false
	}
	littleEndian := false
	switch {
	case raw[0] == 0xFF && raw[1] == 0xFE:
		littleEndian = true
	case raw[0] == 0xFE && raw[1] == 0xFF:
		littleEndian = false
	case raw[0] == '{' && raw[1] == 0:
		littleEndian = true
	case raw[0] == 0 && raw[1] == '{':
		littleEndian = false
	default:
		return nil, false
	}
	words := make([]uint16, 0, len(raw)/2)
	for index := 0; index+1 < len(raw); index += 2 {
		if littleEndian {
			words = append(words, binary.LittleEndian.Uint16(raw[index:index+2]))
		} else {
			words = append(words, binary.BigEndian.Uint16(raw[index:index+2]))
		}
	}
	decoded := strings.TrimPrefix(string(utf16.Decode(words)), "\uFEFF")
	return []byte(decoded), true
}

// writePersistedStoreFile writes and fsyncs a unique same-directory temp file,
// then rotates the last valid canonical file to .bak. Renaming the old file out
// of the way also avoids Windows' replace-existing rename edge cases.
func writePersistedStoreFile(path string, payload []byte) error {
	if !json.Valid(payload) {
		return errors.New("拒绝保存无效的红包监测 JSON")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tempPath := temp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	if written, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	} else if written != len(payload) {
		_ = temp.Close()
		return fmt.Errorf("写入临时文件不完整: %d/%d", written, len(payload))
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("同步临时文件失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := rotatePersistedStoreFile(tempPath, path); err != nil {
		return err
	}
	keepTemp = true // rotate consumed the path; avoid a redundant remove call.
	return nil
}

func rotatePersistedStoreFile(tempPath, path string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.Rename(tempPath, path)
	}
	if err != nil {
		return fmt.Errorf("读取现有存储失败: %w", err)
	}
	_, repaired, normalizeErr := normalizePersistedJSON(raw)
	previousPath := path + ".bak"
	if normalizeErr != nil || repaired {
		previousPath = uniqueCorruptStorePath(path)
	} else if removeErr := os.Remove(previousPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("更新红包监测备份失败: %w", removeErr)
	}
	if err := os.Rename(path, previousPath); err != nil {
		return fmt.Errorf("保留上一份红包监测数据失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		if restoreErr := os.Rename(previousPath, path); restoreErr != nil {
			return fmt.Errorf("替换红包监测数据失败: %v；恢复上一份数据也失败: %w", err, restoreErr)
		}
		return fmt.Errorf("替换红包监测数据失败: %w", err)
	}
	return nil
}

func uniqueCorruptStorePath(path string) string {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	return path + ".corrupt-" + stamp
}
