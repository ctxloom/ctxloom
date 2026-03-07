package remote

import (
	"context"
	"testing"
)

func TestItemType_DirName(t *testing.T) {
	tests := []struct {
		name     string
		itemType ItemType
		want     string
	}{
		{
			name:     "bundle",
			itemType: ItemTypeBundle,
			want:     "bundles",
		},
		{
			name:     "profile",
			itemType: ItemTypeProfile,
			want:     "profiles",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.itemType.DirName(); got != tt.want {
				t.Errorf("ItemType.DirName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestItemType_Plural(t *testing.T) {
	tests := []struct {
		name     string
		itemType ItemType
		want     string
	}{
		{
			name:     "bundle plural",
			itemType: ItemTypeBundle,
			want:     "bundles",
		},
		{
			name:     "profile plural",
			itemType: ItemTypeProfile,
			want:     "profiles",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.itemType.Plural(); got != tt.want {
				t.Errorf("ItemType.Plural() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMockFetcher_ListDir(t *testing.T) {
	fetcher := newMockFetcher()

	// Test empty directory listing
	entries, err := fetcher.ListDir(context.TODO(), "owner", "repo", "path", "ref")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil entries for mock, got %v", entries)
	}
}
