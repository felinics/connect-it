package connectors_test

import (
	"os"
	"strings"
	"testing"

	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/registry"
)

func TestRegisterAll(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r) // 非法 definition 会 panic，测试即失败

	def, ok := r.Get("github")
	if !ok {
		t.Fatal("github 未注册")
	}
	if def.Name != "GitHub" {
		t.Fatalf("got %q", def.Name)
	}
	if len(def.Tools) != 0 {
		t.Fatal("计划 1 阶段 github 应为 catalog_only（无 Tools）")
	}
}

// 目录名必须等于 connector_type 去掉下划线的形式，且一一对应。
func TestDirectoryMatchesRegisteredTypes(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r)

	want := map[string]bool{}
	for _, def := range r.All() {
		want[strings.ReplaceAll(string(def.Type), "_", "")] = true
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			got[e.Name()] = true
		}
	}

	for dir := range got {
		if !want[dir] {
			t.Errorf("目录 %q 没有对应的注册 connector", dir)
		}
	}
	for typ := range want {
		if !got[typ] {
			t.Errorf("注册的 connector %q 没有对应目录", typ)
		}
	}
}
