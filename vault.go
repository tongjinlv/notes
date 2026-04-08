package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Note 与前端 JSON 对齐；Dir 为相对 vault 的路径，如 2026-03/n_xxx（正斜杠）。
type Note struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	UpdatedAt   int64    `json:"updatedAt"`
	Dir         string   `json:"dir"`
	Public      bool     `json:"public"`
	Tags        []string `json:"tags,omitempty"`
	Categories  []string `json:"categories,omitempty"`
}

// noteFMIn 读取 front matter：兼容本站字段与 Hugo 常用字段（date、draft、tags 等）。
type noteFMIn struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Updated    string   `yaml:"updated"`
	Public     *bool    `yaml:"public"`
	Draft      *bool    `yaml:"draft"`
	Tags       []string `yaml:"tags"`
	Categories []string `yaml:"categories"`
}

// hugoFlowQuotedList 序列化为 Hugo 常见行内数组：tags: ["技术", "Hugo"]
type hugoFlowQuotedList []string

func (s hugoFlowQuotedList) MarshalYAML() (interface{}, error) {
	if len(s) == 0 {
		return nil, nil
	}
	seq := &yaml.Node{
		Kind:    yaml.SequenceNode,
		Style:   yaml.FlowStyle,
		Content: make([]*yaml.Node, 0, len(s)),
	}
	for _, x := range s {
		seq.Content = append(seq.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: x,
			Style: yaml.DoubleQuotedStyle,
		})
	}
	return seq, nil
}

// noteFMOut 写入 front matter：带 Hugo 常见的 date / draft（draft 与 public 互斥语义：draft=true 表示未发布）。
type noteFMOut struct {
	ID         string             `yaml:"id"`
	Title      string             `yaml:"title"`
	Updated    string             `yaml:"updated"`
	Date       string             `yaml:"date,omitempty"`
	Public     bool               `yaml:"public"`
	Draft      bool               `yaml:"draft"`
	Tags       hugoFlowQuotedList `yaml:"tags,omitempty"`
	Categories hugoFlowQuotedList `yaml:"categories,omitempty"`
}

type legacyFile struct {
	Notes []Note `json:"notes"`
}

type Vault struct {
	mu   sync.Mutex
	root string
}

func NewVault(root string) *Vault {
	return &Vault{root: root}
}

var (
	yearRe  = regexp.MustCompile(`^\d{4}$`)
	monthRe = regexp.MustCompile(`^(0[1-9]|1[0-2])$`)
	dayRe   = regexp.MustCompile(`^(0[1-9]|[12]\d|3[01])$`)
	yearMonthHyphenRe = regexp.MustCompile(`^(19|20)\d{2}-(0[1-9]|1[0-2])$`)
	// 旧版单段年月目录（无连字符），仍识别以便读取已有数据
	yearMonthCompactRe = regexp.MustCompile(`^(19|20)\d{2}(0[1-9]|1[0-2])$`)
	noteFolderRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
)

func splitFrontMatter(raw []byte) (front []byte, body []byte, hasFM bool) {
	if !bytes.HasPrefix(raw, []byte("---")) {
		return nil, raw, false
	}
	rest := raw[3:]
	if len(rest) > 0 && (rest[0] == '\r' || rest[0] == '\n') {
		if rest[0] == '\r' && len(rest) > 1 && rest[1] == '\n' {
			rest = rest[2:]
		} else {
			rest = rest[1:]
		}
	} else {
		return nil, raw, false
	}
	sep := []byte("\n---")
	idx := bytes.Index(rest, sep)
	if idx < 0 {
		sep = []byte("\r\n---")
		idx = bytes.Index(rest, sep)
	}
	if idx < 0 {
		return nil, raw, false
	}
	front = bytes.TrimSpace(rest[:idx])
	body = rest[idx+len(sep):]
	if len(body) > 0 && (body[0] == '\n' || body[0] == '\r') {
		if bytes.HasPrefix(body, []byte("\r\n")) {
			body = body[2:]
		} else {
			body = body[1:]
		}
	}
	body = bytes.TrimPrefix(body, []byte("\n"))
	return front, body, true
}

func resolvePublicFromFM(public *bool, draft *bool) bool {
	if public != nil {
		return *public
	}
	if draft != nil {
		return !*draft
	}
	return false
}

func parseYAMLDateValue(v interface{}) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	switch x := v.(type) {
	case time.Time:
		return x, true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339,
			"2006-01-02T15:04:05Z07:00",
			"2006-01-02T15:04:05",
			"2006-01-02",
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, s); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

func updatedAtFromFM(updated string, dateFromMap interface{}, modTime time.Time) int64 {
	if strings.TrimSpace(updated) != "" {
		if t, err := time.Parse(time.RFC3339, updated); err == nil {
			return t.UnixMilli()
		}
	}
	if t, ok := parseYAMLDateValue(dateFromMap); ok {
		return t.UnixMilli()
	}
	return modTime.UnixMilli()
}

func normalizeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseNoteMD(raw []byte, folderID string, modTime time.Time) (Note, error) {
	front, body, ok := splitFrontMatter(raw)
	n := Note{Dir: ""}
	if !ok {
		n.ID = folderID
		n.Title = ""
		n.Body = string(raw)
		n.UpdatedAt = modTime.UnixMilli()
		return n, nil
	}
	var fm noteFMIn
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return Note{}, err
	}
	var rawMap map[string]interface{}
	_ = yaml.Unmarshal(front, &rawMap)
	var dateVal interface{}
	if rawMap != nil {
		dateVal = rawMap["date"]
	}
	if fm.ID != "" {
		n.ID = fm.ID
	} else {
		n.ID = folderID
	}
	n.Title = fm.Title
	n.UpdatedAt = updatedAtFromFM(fm.Updated, dateVal, modTime)
	n.Public = resolvePublicFromFM(fm.Public, fm.Draft)
	n.Tags = normalizeStringSlice(fm.Tags)
	n.Categories = normalizeStringSlice(fm.Categories)
	n.Body = string(body)
	return n, nil
}

func composeNoteMD(n Note, updated time.Time) ([]byte, error) {
	ut := updated.UTC()
	fm := noteFMOut{
		ID:         n.ID,
		Title:      n.Title,
		Updated:    ut.Format(time.RFC3339),
		Date:       ut.Format("2006-01-02"),
		Public:     n.Public,
		Draft:      !n.Public,
		Tags:       hugoFlowQuotedList(normalizeStringSlice(n.Tags)),
		Categories: hugoFlowQuotedList(normalizeStringSlice(n.Categories)),
	}
	head, err := yaml.Marshal(fm)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n\n")
	b.WriteString(n.Body)
	if len(n.Body) > 0 && !strings.HasSuffix(n.Body, "\n") {
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

func isNoteLayoutDir(parts []string) bool {
	switch len(parts) {
	case 2:
		ym := yearMonthHyphenRe.MatchString(parts[0]) || yearMonthCompactRe.MatchString(parts[0])
		return ym && noteFolderRe.MatchString(parts[1])
	case 3:
		return yearRe.MatchString(parts[0]) && monthRe.MatchString(parts[1]) && noteFolderRe.MatchString(parts[2])
	case 4:
		return yearRe.MatchString(parts[0]) && monthRe.MatchString(parts[1]) && dayRe.MatchString(parts[2]) && noteFolderRe.MatchString(parts[3])
	default:
		return false
	}
}

func noteLayoutLeafID(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func (v *Vault) abs(rel string) string {
	return filepath.Join(v.root, filepath.FromSlash(rel))
}

func safeNoteID(id string) bool {
	if id == "" || len(id) > 160 {
		return false
	}
	return !strings.ContainsAny(id, `/\:`)
}

// 侧栏顺序持久化（与笔记 md 同级仓库根下）；无此文件时 List 按 dir+id 排序
const sidebarOrderFile = ".notes-sidebar-order.json"

func sortNotesByDirID(notes []Note) {
	sort.Slice(notes, func(i, j int) bool {
		di, dj := notes[i].Dir, notes[j].Dir
		if di != dj {
			return di > dj
		}
		return notes[i].ID > notes[j].ID
	})
}

func (v *Vault) sidebarOrderPath() string {
	return filepath.Join(v.root, sidebarOrderFile)
}

func (v *Vault) loadSidebarOrderUnlocked() []string {
	data, err := os.ReadFile(v.sidebarOrderPath())
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var ids []string
	if json.Unmarshal(data, &ids) != nil {
		return nil
	}
	return ids
}

func (v *Vault) saveSidebarOrderUnlocked(ids []string) error {
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(v.sidebarOrderPath(), data, 0o644)
}

func sidebarOrderWithoutID(ids []string, drop string) []string {
	out := ids[:0]
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}

func (v *Vault) applySidebarOrderUnlocked(notes []Note, order []string) []Note {
	byID := make(map[string]Note, len(notes))
	for _, n := range notes {
		byID[n.ID] = n
	}
	seen := make(map[string]bool, len(notes))
	out := make([]Note, 0, len(notes))
	for _, id := range order {
		if n, ok := byID[id]; ok {
			out = append(out, n)
			seen[id] = true
		}
	}
	var rest []Note
	for _, n := range notes {
		if !seen[n.ID] {
			rest = append(rest, n)
		}
	}
	sortNotesByDirID(rest)
	return append(out, rest...)
}

func (v *Vault) listNotesRawUnlocked() ([]Note, error) {
	var notes []Note
	err := filepath.WalkDir(v.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !shouldProcessNoteMarkdownPath(path) {
			return nil
		}
		rel, e := filepath.Rel(v.root, path)
		if e != nil {
			return e
		}
		dirRel := filepath.ToSlash(filepath.Dir(rel))
		parts := strings.Split(dirRel, "/")
		if !isNoteLayoutDir(parts) {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		info, _ := d.Info()
		mt := time.Now()
		if info != nil {
			mt = info.ModTime()
		}
		note, e := parseNoteMD(raw, noteLayoutLeafID(parts), mt)
		if e != nil {
			return nil
		}
		note.Dir = dirRel
		notes = append(notes, note)
		return nil
	})
	return notes, err
}

func (v *Vault) sidebarInsertUnlocked(newID, beforeID string) {
	ids := v.loadSidebarOrderUnlocked()
	ids = sidebarOrderWithoutID(ids, newID)
	if len(ids) == 0 {
		all, err := v.listNotesRawUnlocked()
		if err == nil {
			sortNotesByDirID(all)
			for _, n := range all {
				if n.ID != newID {
					ids = append(ids, n.ID)
				}
			}
		}
	}
	if beforeID != "" {
		idx := -1
		for i, x := range ids {
			if x == beforeID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			ids = append(ids[:idx], append([]string{newID}, ids[idx:]...)...)
		} else {
			ids = append([]string{newID}, ids...)
		}
	} else {
		ids = append([]string{newID}, ids...)
	}
	_ = v.saveSidebarOrderUnlocked(ids)
}

func (v *Vault) sidebarRemoveUnlocked(id string) {
	ids := v.loadSidebarOrderUnlocked()
	if len(ids) == 0 {
		return
	}
	_ = v.saveSidebarOrderUnlocked(sidebarOrderWithoutID(ids, id))
}

func (v *Vault) List() ([]Note, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	notes, err := v.listNotesRawUnlocked()
	if err != nil {
		return nil, err
	}
	order := v.loadSidebarOrderUnlocked()
	if len(order) == 0 {
		sortNotesByDirID(notes)
		return notes, nil
	}
	return v.applySidebarOrderUnlocked(notes, order), nil
}

func (v *Vault) Create(title, body, beforeID string, public bool, tags, categories []string) (Note, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	id := newNoteID()
	t := time.Now()
	y, m, _ := t.Date()
	dirRel := filepath.ToSlash(filepath.Join(
		fmt.Sprintf("%04d-%02d", y, int(m)),
		id,
	))
	full := v.abs(dirRel)
	if err := os.MkdirAll(full, 0o755); err != nil {
		return Note{}, err
	}
	n := Note{
		ID: id, Title: title, Body: body, UpdatedAt: t.UnixMilli(), Dir: dirRel, Public: public,
		Tags: normalizeStringSlice(tags), Categories: normalizeStringSlice(categories),
	}
	raw, err := composeNoteMD(n, t)
	if err != nil {
		return Note{}, err
	}
	if err := os.WriteFile(filepath.Join(full, noteMarkdownFile), raw, 0o644); err != nil {
		return Note{}, err
	}
	before := beforeID
	if before != "" && !safeNoteID(before) {
		before = ""
	}
	v.sidebarInsertUnlocked(id, before)
	invalidatePublicPostCache()
	return n, nil
}

func normalizedBodyForCompare(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func noteUpdatePayloadEqual(existing Note, title, body string, public bool, tags, categories []string) bool {
	if strings.TrimSpace(existing.Title) != strings.TrimSpace(title) {
		return false
	}
	if normalizedBodyForCompare(existing.Body) != normalizedBodyForCompare(body) {
		return false
	}
	if existing.Public != public {
		return false
	}
	if !slices.Equal(normalizeStringSlice(existing.Tags), normalizeStringSlice(tags)) {
		return false
	}
	if !slices.Equal(normalizeStringSlice(existing.Categories), normalizeStringSlice(categories)) {
		return false
	}
	return true
}

func (v *Vault) readNoteMDInDirUnlocked(dirRel string) (Note, error) {
	absDir := v.abs(dirRel)
	path, ok := resolveNoteMarkdownPath(absDir)
	if !ok {
		return Note{}, os.ErrNotExist
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Note{}, err
	}
	mt := time.Now()
	if info, err := os.Stat(path); err == nil && info != nil {
		mt = info.ModTime()
	}
	parts := strings.Split(filepath.ToSlash(dirRel), "/")
	note, err := parseNoteMD(raw, noteLayoutLeafID(parts), mt)
	if err != nil {
		return Note{}, err
	}
	note.Dir = dirRel
	return note, nil
}

func (v *Vault) Update(id, title, body string, public bool, tags, categories []string) (Note, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	dirRel, err := v.findDirByIDUnlocked(id)
	if err != nil {
		return Note{}, err
	}
	existing, rerr := v.readNoteMDInDirUnlocked(dirRel)
	if rerr == nil && noteUpdatePayloadEqual(existing, title, body, public, tags, categories) {
		return existing, nil
	}
	t := time.Now()
	n := Note{
		ID: id, Title: title, Body: body, UpdatedAt: t.UnixMilli(), Dir: dirRel, Public: public,
		Tags: normalizeStringSlice(tags), Categories: normalizeStringSlice(categories),
	}
	raw, err := composeNoteMD(n, t)
	if err != nil {
		return Note{}, err
	}
	full := filepath.Join(v.abs(dirRel), noteMarkdownFile)
	if err := os.WriteFile(full, raw, 0o644); err != nil {
		return Note{}, err
	}
	_ = os.Remove(filepath.Join(v.abs(dirRel), legacyNoteMarkdownFile))
	invalidatePublicPostCache()
	return n, nil
}

func (v *Vault) findDirByIDUnlocked(id string) (string, error) {
	if !safeNoteID(id) {
		return "", os.ErrNotExist
	}
	var found string
	_ = filepath.WalkDir(v.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !shouldProcessNoteMarkdownPath(path) {
			return nil
		}
		rel, e := filepath.Rel(v.root, path)
		if e != nil {
			return nil
		}
		dirRel := filepath.ToSlash(filepath.Dir(rel))
		parts := strings.Split(dirRel, "/")
		if !isNoteLayoutDir(parts) {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return nil
		}
		info, _ := d.Info()
		mt := time.Now()
		if info != nil {
			mt = info.ModTime()
		}
		note, e := parseNoteMD(raw, noteLayoutLeafID(parts), mt)
		if e != nil {
			return nil
		}
		if note.ID == id || noteLayoutLeafID(parts) == id {
			found = dirRel
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}

func (v *Vault) Delete(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	dirRel, err := v.findDirByIDUnlocked(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(v.abs(dirRel)); err != nil {
		return err
	}
	v.sidebarRemoveUnlocked(id)
	invalidatePublicPostCache()
	return nil
}

func (v *Vault) SaveImage(noteID string, data []byte, ext string) (fileName string, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	dirRel, err := v.findDirByIDUnlocked(noteID)
	if err != nil {
		return "", err
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	fileName = fmt.Sprintf("image-%d-%s%s", time.Now().UnixMilli(), hex.EncodeToString(b), ext)
	full := filepath.Join(v.abs(dirRel), fileName)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	return fileName, nil
}

func sanitizeAttachmentBase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = filepath.Base(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.', r >= 0x0080: // 含中文等
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return ""
	}
	if ext := filepath.Ext(out); ext != "" {
		base := strings.TrimSuffix(out, ext)
		if len(base) > 120 {
			base = base[:120]
		}
		return base
	}
	if len(out) > 120 {
		return out[:120]
	}
	return out
}

func sanitizeAttachmentExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" || ext[0] != '.' || len(ext) > 32 {
		return ""
	}
	for _, r := range ext[1:] {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		return ""
	}
	return ext
}

// SaveAttachment 将任意二进制写入当前笔记目录，文件名由建议名与随机后缀组成。
func (v *Vault) SaveAttachment(noteID string, data []byte, suggestedName string) (fileName string, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	dirRel, err := v.findDirByIDUnlocked(noteID)
	if err != nil {
		return "", err
	}
	base := filepath.Base(strings.TrimSpace(suggestedName))
	if base == "" || base == "." {
		base = "file"
	}
	ext := filepath.Ext(base)
	nameOnly := strings.TrimSuffix(base, ext)
	safeBase := sanitizeAttachmentBase(nameOnly)
	if safeBase == "" {
		safeBase = "file"
	}
	safeExt := sanitizeAttachmentExt(ext)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	fileName = fmt.Sprintf("%s-%s%s", safeBase, hex.EncodeToString(b), safeExt)
	full := filepath.Join(v.abs(dirRel), fileName)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	return fileName, nil
}

func (v *Vault) Root() string {
	return v.root
}

func (v *Vault) resolveVaultPath(rel string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, "../") {
		return "", os.ErrNotExist
	}
	full := filepath.Clean(filepath.Join(v.root, filepath.FromSlash(rel)))
	absRoot, err := filepath.Abs(v.root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	sep := string(os.PathSeparator)
	if absFull != absRoot && !strings.HasPrefix(absFull+sep, absRoot+sep) {
		return "", os.ErrNotExist
	}
	return absFull, nil
}

func migrateLegacyJSON(vaultRoot, jsonPath string) error {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return err
	}
	var leg legacyFile
	if err := json.Unmarshal(raw, &leg); err != nil {
		return err
	}
	if len(leg.Notes) == 0 {
		return nil
	}
	_ = os.MkdirAll(vaultRoot, 0o755)
	for _, x := range leg.Notes {
		n := x
		if !noteFolderRe.MatchString(n.ID) {
			n.ID = newNoteID()
		}
		t := time.UnixMilli(n.UpdatedAt)
		if n.UpdatedAt == 0 {
			t = time.Now()
		}
		y, m, _ := t.Date()
		dirRel := filepath.ToSlash(filepath.Join(
			fmt.Sprintf("%04d-%02d", y, int(m)),
			n.ID,
		))
		full := filepath.Join(vaultRoot, filepath.FromSlash(dirRel))
		if err := os.MkdirAll(full, 0o755); err != nil {
			return err
		}
		n.Dir = dirRel
		rawMD, err := composeNoteMD(Note{ID: n.ID, Title: n.Title, Body: n.Body}, t)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(full, noteMarkdownFile), rawMD, 0o644); err != nil {
			return err
		}
	}
	bak := jsonPath + ".bak"
	_ = os.Rename(jsonPath, bak)
	return nil
}

func vaultHasAnyNote(vaultRoot string) bool {
	found := false
	_ = filepath.WalkDir(vaultRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if isNoteMarkdownFilename(filepath.Base(path)) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
