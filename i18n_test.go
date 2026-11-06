package main

import "testing"

func TestLanguageSelection(t *testing.T) {
	old := currentLang
	defer func() { setLanguage(old); refreshAppTabs() }()

	if l, ok := parseLang("ko"); !ok || l != langKO {
		t.Fatalf("parseLang(ko) = %v, %v", l, ok)
	}
	if l, ok := parseLang("zh-CN"); !ok || l != langZH {
		t.Fatalf("parseLang(zh-CN) = %v, %v", l, ok)
	}
	if _, ok := parseLang("xx"); ok {
		t.Fatal("parseLang(xx) must fail")
	}

	args := initLanguage([]string{"-lang", "ko", "lol", "leagues"})
	if len(args) != 2 || args[0] != "lol" || args[1] != "leagues" {
		t.Fatalf("args after -lang = %v", args)
	}
	if got := tr("leagues"); got != "리그" {
		t.Fatalf("ko leagues = %q", got)
	}
	if appTabs[0] != "라이브" {
		t.Fatalf("ko tab = %q", appTabs[0])
	}

	setLanguage(langZH)
	if got := tr("leagues"); got != "联赛" {
		t.Fatalf("zh leagues = %q", got)
	}
	if got := tr("preview"); got != "预览" {
		t.Fatalf("zh preview = %q", got)
	}
	setLanguage(langEN)
	if got := tr("leagues"); got != "Leagues" {
		t.Fatalf("en leagues = %q", got)
	}
}
