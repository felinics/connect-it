package onedrive

import (
	"reflect"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// The published Tool set is pinned in CI, not in the opt-in smoke harness.
func TestDefinitionPublishesExactlyTheReviewedTools(t *testing.T) {
	got := make([]string, 0, len(Definition.Tools))
	for _, tool := range Definition.Tools {
		got = append(got, tool.ID)
	}
	if want := []string{
		"list_drive_items",
		"upload_file",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Tools = %#v, want %#v", got, want)
	}
}

func TestUploadFileDeclaresRawJSONInputCeiling(t *testing.T) {
	for _, tool := range Definition.Tools {
		if tool.ID != "upload_file" {
			continue
		}
		if tool.MaxInputBytes != connector.AbsoluteMaxInputBytes {
			t.Fatalf(
				"upload_file MaxInputBytes = %d, want absolute ceiling %d",
				tool.MaxInputBytes,
				connector.AbsoluteMaxInputBytes,
			)
		}
		return
	}
	t.Fatal("upload_file tool is missing")
}
