package main

import (
	"os"
	"path/filepath"
)

// 与 Hugo leaf bundle 一致，正文使用 index.md；旧数据可能仍为 note.md。
const (
	noteMarkdownFile       = "index.md"
	legacyNoteMarkdownFile = "note.md"
)

func isNoteMarkdownFilename(name string) bool {
	return name == noteMarkdownFile || name == legacyNoteMarkdownFile
}

// resolveNoteMarkdownPath 在笔记目录 absDir 下优先使用 index.md，否则回退 note.md。
func resolveNoteMarkdownPath(absDir string) (absPath string, ok bool) {
	p1 := filepath.Join(absDir, noteMarkdownFile)
	if st, err := os.Stat(p1); err == nil && !st.IsDir() {
		return p1, true
	}
	p2 := filepath.Join(absDir, legacyNoteMarkdownFile)
	if st, err := os.Stat(p2); err == nil && !st.IsDir() {
		return p2, true
	}
	return "", false
}

// shouldProcessNoteMarkdownPath 在目录遍历时：同目录有 index.md 则忽略 note.md。
func shouldProcessNoteMarkdownPath(path string) bool {
	base := filepath.Base(path)
	if base != noteMarkdownFile && base != legacyNoteMarkdownFile {
		return false
	}
	if base == legacyNoteMarkdownFile {
		idx := filepath.Join(filepath.Dir(path), noteMarkdownFile)
		if _, err := os.Stat(idx); err == nil {
			return false
		}
	}
	return true
}
