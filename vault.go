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
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Note 与前端 JSON 对齐；Dir 为相对 vault 的路径，如 2026/03/24/n_xxx（正斜杠）
type Note struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	UpdatedAt  int64  `json:"updatedAt"`
	Dir        string `json:"dir"`
	Public bool `json:"public"`
}

type noteFM struct {
	ID         string `yaml:"id"`
	Title      string `yaml:"title"`
	Updated string `yaml:"updated"`
	Public  bool   `yaml:"public"`
}

type legacyFile struct {
	Notes []Note `json:"notes"`
}

type Vault struct {
	mu         sync.Mutex
	root       string
	passphrase string // 非空则 note.md 以 AES-GCM 密文存储；勿与仓库一起提交到 Git
}

func NewVault(root, passphrase string) *Vault {
	return &Vault{root: root, passphrase: passphrase}
}

var (
	yearRe  = regexp.MustCompile(`^\d{4}$`)
	monthRe = regexp.MustCompile(`^(0[1-9]|1[0-2])$`)
	dayRe   = regexp.MustCompile(`^(0[1-9]|[12]\d|3[01])$`)
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

func parseNoteMD(raw []byte, folderID string, modTime time.Time, passphrase string) (Note, error) {
	raw, err := unwrapVaultBlob(raw, passphrase)
	if err != nil {
		return Note{}, err
	}
	front, body, ok := splitFrontMatter(raw)
	n := Note{Dir: ""}
	if !ok {
		n.ID = folderID
		n.Title = ""
		n.Body = string(raw)
		n.UpdatedAt = modTime.UnixMilli()
		return n, nil
	}
	var fm noteFM
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return Note{}, err
	}
	if fm.ID != "" {
		n.ID = fm.ID
	} else {
		n.ID = folderID
	}
	n.Title = fm.Title
	if fm.Updated != "" {
		if t, err := time.Parse(time.RFC3339, fm.Updated); err == nil {
			n.UpdatedAt = t.UnixMilli()
		} else {
			n.UpdatedAt = modTime.UnixMilli()
		}
	} else {
		n.UpdatedAt = modTime.UnixMilli()
	}
	n.Public = fm.Public
	n.Body = string(body)
	return n, nil
}

func composeNoteMD(n Note, updated time.Time) ([]byte, error) {
	fm := noteFM{
		ID:      n.ID,
		Title:   n.Title,
		Updated: updated.UTC().Format(time.RFC3339),
		Public:  n.Public,
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
	if len(parts) != 4 {
		return false
	}
	if !yearRe.MatchString(parts[0]) || !monthRe.MatchString(parts[1]) || !dayRe.MatchString(parts[2]) {
		return false
	}
	return noteFolderRe.MatchString(parts[3])
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
		if filepath.Base(path) != "note.md" {
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
		note, e := parseNoteMD(raw, parts[3], mt, v.passphrase)
		if e != nil {
			return e
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

func (v *Vault) Create(title, body, beforeID string, public bool) (Note, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	id := newNoteID()
	t := time.Now()
	y, m, d := t.Date()
	dirRel := filepath.ToSlash(filepath.Join(
		fmt.Sprintf("%04d", y),
		fmt.Sprintf("%02d", int(m)),
		fmt.Sprintf("%02d", d),
		id,
	))
	full := v.abs(dirRel)
	if err := os.MkdirAll(full, 0o755); err != nil {
		return Note{}, err
	}
	n := Note{ID: id, Title: title, Body: body, UpdatedAt: t.UnixMilli(), Dir: dirRel, Public: public}
	raw, err := composeNoteMD(n, t)
	if err != nil {
		return Note{}, err
	}
	raw, err = wrapVaultBlob(raw, v.passphrase)
	if err != nil {
		return Note{}, err
	}
	if err := os.WriteFile(filepath.Join(full, "note.md"), raw, 0o644); err != nil {
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

func (v *Vault) Update(id, title, body string, public bool) (Note, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	dirRel, err := v.findDirByIDUnlocked(id)
	if err != nil {
		return Note{}, err
	}
	t := time.Now()
	n := Note{ID: id, Title: title, Body: body, UpdatedAt: t.UnixMilli(), Dir: dirRel, Public: public}
	raw, err := composeNoteMD(n, t)
	if err != nil {
		return Note{}, err
	}
	raw, err = wrapVaultBlob(raw, v.passphrase)
	if err != nil {
		return Note{}, err
	}
	full := filepath.Join(v.abs(dirRel), "note.md")
	if err := os.WriteFile(full, raw, 0o644); err != nil {
		return Note{}, err
	}
	invalidatePublicPostCache()
	return n, nil
}

func (v *Vault) findDirByIDUnlocked(id string) (string, error) {
	if !safeNoteID(id) {
		return "", os.ErrNotExist
	}
	var found string
	_ = filepath.WalkDir(v.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) != "note.md" {
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
		note, e := parseNoteMD(raw, parts[3], mt, v.passphrase)
		if e != nil {
			return e
		}
		if note.ID == id || parts[3] == id {
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
	out, err := wrapVaultBlob(data, v.passphrase)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, out, 0o644); err != nil {
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

func migrateLegacyJSON(vaultRoot, jsonPath, passphrase string) error {
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
		y, m, d := t.Date()
		dirRel := filepath.ToSlash(filepath.Join(
			fmt.Sprintf("%04d", y),
			fmt.Sprintf("%02d", int(m)),
			fmt.Sprintf("%02d", d),
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
		rawMD, err = wrapVaultBlob(rawMD, passphrase)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(full, "note.md"), rawMD, 0o644); err != nil {
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
		if filepath.Base(path) == "note.md" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
