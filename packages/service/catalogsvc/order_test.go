package catalogsvc

import (
	"slices"
	"testing"
)

func TestSortItems(t *testing.T) {
	items := []Item{
		{Type: "zzz"},
		{Type: "slack"},
		{Type: "github"},
		{Type: "aaa"},
	}

	sortItems(items)

	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.Type)
	}
	want := []string{"github", "slack", "aaa", "zzz"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
