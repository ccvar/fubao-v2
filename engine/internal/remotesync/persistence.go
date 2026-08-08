package remotesync

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

type privateJSONCandidate struct {
	path  string
	label string
	mtime time.Time
}

// loadRecoverablePrivateJSON keeps Windows text encoding or interrupted-write
// damage from disabling remote sync. Secrets remain in permission-restricted
// native files; neither damaged bytes nor recovered token values are logged.
func loadRecoverablePrivateJSON(path, label string, destination, fallback any) (bool, error) {
	resetPayload, marshalErr := json.Marshal(fallback)
	if marshalErr != nil {
		return false, fmt.Errorf("准备%s默认值失败: %w", label, marshalErr)
	}
	candidates, err := privateJSONCandidates(path)
	if err != nil {
		return false, fmt.Errorf("读取%s失败: %w", label, err)
	}
	if len(candidates) == 0 {
		return false, nil
	}

	var firstParseErr error
	for _, candidate := range candidates {
		raw, readErr := os.ReadFile(candidate.path)
		if readErr != nil {
			if candidate.path == path {
				return false, fmt.Errorf("读取%s失败: %w", label, readErr)
			}
			continue
		}
		normalized, repaired, normalizeErr := normalizePrivateJSON(raw)
		if normalizeErr != nil {
			if firstParseErr == nil {
				firstParseErr = normalizeErr
			}
			continue
		}
		// json.Unmarshal can partially update a destination before reporting a
		// type error. Reapply the safe defaults before every candidate so a bad
		// primary file cannot leak partial fields into a recovered backup.
		if resetErr := json.Unmarshal(resetPayload, destination); resetErr != nil {
			return false, fmt.Errorf("准备%s默认值失败: %w", label, resetErr)
		}
		if unmarshalErr := json.Unmarshal(normalized, destination); unmarshalErr != nil {
			if firstParseErr == nil {
				firstParseErr = unmarshalErr
			}
			continue
		}
		if candidate.path != path || repaired {
			if writeErr := writePrivateJSONFile(path, append(normalized, '\n')); writeErr != nil {
				return false, fmt.Errorf("恢复%s失败: %w", label, writeErr)
			}
			fmt.Fprintf(os.Stderr, "%s已从%s恢复\n", label, candidate.label)
		}
		return true, nil
	}

	// Remote-sync configuration can be re-enrolled and its safe outbox can be
	// rebuilt. Preserve every unusable original, then keep the feature usable
	// in an explicit unconfigured/empty state instead of failing its RPC setup.
	if writeErr := writePrivateJSONFile(path, append(resetPayload, '\n')); writeErr != nil {
		return false, fmt.Errorf("重建%s失败: %w", label, writeErr)
	}
	if unmarshalErr := json.Unmarshal(resetPayload, destination); unmarshalErr != nil {
		return false, fmt.Errorf("重建%s失败: %w", label, unmarshalErr)
	}
	if firstParseErr == nil {
		firstParseErr = errors.New("未找到可恢复的 JSON 快照")
	}
	fmt.Fprintf(os.Stderr, "%s无法解析，已隔离损坏文件并重建安全默认值: %v\n", label, firstParseErr)
	return true, nil
}

func privateJSONCandidates(path string) ([]privateJSONCandidate, error) {
	items := make([]privateJSONCandidate, 0, 4)
	fallbacks := make([]privateJSONCandidate, 0, 3)
	appendCandidate := func(target *[]privateJSONCandidate, candidatePath, label string) error {
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
			if candidatePath == path {
				return errors.New("数据路径是目录")
			}
			return nil
		}
		*target = append(*target, privateJSONCandidate{path: candidatePath, label: label, mtime: info.ModTime()})
		return nil
	}

	if err := appendCandidate(&items, path, "主文件可恢复内容"); err != nil {
		return nil, err
	}
	if err := appendCandidate(&fallbacks, path+".tmp", "未完成的临时快照"); err != nil {
		return nil, err
	}
	tempMatches, _ := filepath.Glob(path + ".tmp-*")
	for _, match := range tempMatches {
		info, statErr := os.Stat(match)
		if statErr == nil && !info.IsDir() {
			fallbacks = append(fallbacks, privateJSONCandidate{path: match, label: "未完成的临时快照", mtime: info.ModTime()})
		}
	}
	if err := appendCandidate(&fallbacks, path+".bak", "上一份有效备份"); err != nil {
		return nil, err
	}
	sort.SliceStable(fallbacks, func(i, j int) bool { return fallbacks[i].mtime.After(fallbacks[j].mtime) })
	return append(items, fallbacks...), nil
}

// normalizePrivateJSON only accepts a repair when the complete result is valid
// JSON. It covers UTF-8 BOM, UTF-16 LE/BE and NUL padding/interleaving observed
// in Windows-local files without guessing at configuration fields.
func normalizePrivateJSON(raw []byte) ([]byte, bool, error) {
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
	combined := bytes.Trim(raw, "\x00 \t\r\n")
	combined = bytes.TrimPrefix(combined, []byte{0xEF, 0xBB, 0xBF})
	combined = bytes.ReplaceAll(combined, []byte{0}, nil)
	combined = bytes.TrimPrefix(bytes.TrimSpace(combined), []byte{0xEF, 0xBB, 0xBF})
	if normalized, repaired, ok := try(combined, true); ok {
		return normalized, repaired, nil
	}
	if decoded, ok := decodePrivateUTF16(raw); ok {
		if normalized, repaired, valid := try(decoded, true); valid {
			return normalized, repaired, nil
		}
	}
	var probe any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &probe); err != nil {
		return nil, false, err
	}
	return nil, false, errors.New("内容不是有效 JSON")
}

func decodePrivateUTF16(raw []byte) ([]byte, bool) {
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

// writePrivateJSONFile fsyncs a unique same-directory temp file and removes
// the destination before the final rename. That avoids replace-existing rename
// failures on Windows while preserving the previous valid snapshot or damaged
// original under a permission-inheriting sibling path.
func writePrivateJSONFile(path string, payload []byte) error {
	if !json.Valid(payload) {
		return errors.New("拒绝保存无效 JSON")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return err
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
		return err
	}
	if written, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	} else if written != len(payload) {
		_ = temp.Close()
		return fmt.Errorf("写入临时文件不完整: %d/%d", written, len(payload))
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := rotatePrivateJSONFile(tempPath, path); err != nil {
		return err
	}
	keepTemp = true
	return nil
}

func rotatePrivateJSONFile(tempPath, path string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.Rename(tempPath, path)
	}
	if err != nil {
		return err
	}
	_, repaired, normalizeErr := normalizePrivateJSON(raw)
	previousPath := path + ".bak"
	if normalizeErr != nil || repaired {
		previousPath = path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	} else if removeErr := os.Remove(previousPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	if err := os.Rename(path, previousPath); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if restoreErr := os.Rename(previousPath, path); restoreErr != nil {
			return fmt.Errorf("替换私有 JSON 失败: %v；恢复上一份数据也失败: %w", err, restoreErr)
		}
		return err
	}
	return nil
}
