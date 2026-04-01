package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseNoteMD_HugoDraft(t *testing.T) {
	mod := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := `---
title: 写了个vscode插件
date: 2025-12-06
draft: false
tags: ["技术", "Hugo"]
categories: ["技术教程"]
---

正文
`
	n, err := parseNoteMD([]byte(raw), "n_test", mod)
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "写了个vscode插件" {
		t.Fatalf("title: got %q", n.Title)
	}
	if !n.Public {
		t.Fatal("draft: false 应视为公开（public=true）")
	}
	// date 作为时间
	if n.UpdatedAt == mod.UnixMilli() {
		t.Fatal("expected date from front matter")
	}
}

func TestParseNoteMD_DraftTrue(t *testing.T) {
	mod := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := `---
title: x
draft: true
---
`
	n, err := parseNoteMD([]byte(raw), "n_x", mod)
	if err != nil {
		t.Fatal(err)
	}
	if n.Public {
		t.Fatal("draft: true 应不公开")
	}
}

func TestParseNoteMD_PublicWinsOverDraft(t *testing.T) {
	mod := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := `---
title: x
public: false
draft: false
---
`
	n, err := parseNoteMD([]byte(raw), "n_x", mod)
	if err != nil {
		t.Fatal(err)
	}
	if n.Public {
		t.Fatal("同时存在时以 public 为准")
	}
}

func TestComposeNoteMD_IncludesDraftAndDate(t *testing.T) {
	n := Note{ID: "n_1", Title: "t", Body: "b", Public: true}
	b, err := composeNoteMD(n, time.Date(2025, 12, 6, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "draft: false") {
		t.Fatalf("expected draft: false when public, got:\n%s", s)
	}
	if !strings.Contains(s, "2025-12-06") || !strings.Contains(s, "date:") {
		t.Fatalf("expected date line, got:\n%s", s)
	}
}

func TestComposeNoteMD_TagsCategoriesFlowStyle(t *testing.T) {
	n := Note{
		ID:         "n_1",
		Title:      "t",
		Body:       "b",
		Public:     true,
		Tags:       []string{"技术", "Hugo"},
		Categories: []string{"技术教程"},
	}
	b, err := composeNoteMD(n, time.Date(2025, 12, 6, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `tags: ["技术", "Hugo"]`) {
		t.Fatalf("expected Hugo-style tags line, got:\n%s", s)
	}
	if !strings.Contains(s, `categories: ["技术教程"]`) {
		t.Fatalf("expected Hugo-style categories line, got:\n%s", s)
	}
}
