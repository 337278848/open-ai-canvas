package protocol

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOfficialLxmoneVideoResultURLs(t *testing.T) {
	providers := []string{
		"lxmone-wan-videos", "lxmone-wan-channel-s", "lxmone-seedance-videos", "lxmone-h3-max-videos",
		"lxmone-sd-videos", "lxmone-sd-mini-videos", "lxmone-grok-videos", "lxmone-h3-workflow",
	}
	const directURL = "https://cdn.example.com/generated.mp4?signature=test"
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			adapter := officialPackageAdapter(t, "lxmone-video-suite.yingce-plugin", provider)
			for _, tc := range []struct {
				name    string
				payload map[string]any
			}{
				{"direct_over_relative", map[string]any{"id": "video-test", "status": "completed", "metadata": map[string]any{"url": "/v1/videos/video-test/content", "direct_url": directURL}}},
				{"nested_direct_over_relative", map[string]any{"metadata": map[string]any{"url": "/v1/videos/video-test/content"}, "data": map[string]any{"id": "video-test", "status": "completed", "metadata": map[string]any{"url": "/v1/videos/video-test/content", "direct_url": directURL}}}},
				{"absolute_url_without_direct", map[string]any{"id": "video-test", "status": "completed", "metadata": map[string]any{"url": directURL}}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body, err := json.Marshal(tc.payload)
					if err != nil {
						t.Fatal(err)
					}
					created, err := adapter.ParseCreate(context.Background(), body)
					if err != nil {
						t.Fatal(err)
					}
					polled, err := adapter.ParsePoll(context.Background(), PollContext{TaskID: "video-test"}, body)
					if err != nil {
						t.Fatal(err)
					}
					if created.Status != StatusSucceeded || polled.Status != StatusSucceeded || created.TaskID != "video-test" || polled.TaskID != "video-test" {
						t.Fatalf("unexpected completion: create=%#v poll=%#v", created, polled)
					}
					for _, result := range []*Result{created.Result, polled.Result} {
						if result == nil || len(result.Videos) != 1 || result.Videos[0].URL != directURL {
							t.Fatalf("expected direct download URL, got %#v", result)
						}
					}
				})
			}
		})
	}
}

func TestLxmoneH3WorkflowCreateUsesGenerationsReferences(t *testing.T) {
	adapter := officialPackageAdapter(t, "lxmone-video-suite.yingce-plugin", "lxmone-h3-workflow")
	for _, tc := range []struct {
		name   string
		images []MediaReference
		first  any
		last   any
		refs   []any
	}{
		{
			name:   "unroled references stay references",
			images: []MediaReference{{URL: "https://cdn.example/a.png", Order: 1}, {URL: "https://cdn.example/b.png", Order: 2}},
			refs:   []any{"https://cdn.example/a.png", "https://cdn.example/b.png"},
		},
		{
			name: "explicit frames leave references",
			images: []MediaReference{
				{URL: "https://cdn.example/ref.png", Role: "reference_image", Order: 1},
				{URL: "https://cdn.example/last.png", Role: "last_frame", Order: 3},
				{URL: "https://cdn.example/first.png", Role: "first_frame", Order: 2},
			},
			first: "https://cdn.example/first.png",
			last:  "https://cdn.example/last.png",
			refs:  []any{"https://cdn.example/ref.png"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create, err := adapter.BuildCreate(context.Background(), RequestContext{Request: GenerationRequest{
				Model: "minimax-h3-c", Prompt: "test", Duration: 4, Resolution: "720p", AspectRatio: "9:16", Images: tc.images,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if create.Path != "/v1/videos/generations" {
				t.Fatalf("path = %s", create.Path)
			}
			body, _ := create.Body.(map[string]any)
			if body["duration_seconds"] != 4 {
				t.Fatalf("duration_seconds = %#v", body["duration_seconds"])
			}
			if _, exists := body["seconds"]; exists {
				t.Fatalf("seconds must be absent: %#v", body["seconds"])
			}
			if _, exists := body["input_reference"]; exists {
				t.Fatalf("input_reference must be absent: %#v", body["input_reference"])
			}
			if body["first_frame"] != tc.first || body["last_frame"] != tc.last {
				t.Fatalf("frames = %#v / %#v", body["first_frame"], body["last_frame"])
			}
			refs, _ := body["reference_images"].([]any)
			if len(refs) != len(tc.refs) {
				t.Fatalf("references = %#v", body["reference_images"])
			}
			for index := range tc.refs {
				if refs[index] != tc.refs[index] {
					t.Fatalf("references = %#v", refs)
				}
			}
		})
	}
}

func TestLxmoneH3MaxCreateMapsExplicitFramesWithoutGuessing(t *testing.T) {
	adapter := officialPackageAdapter(t, "lxmone-video-suite.yingce-plugin", "lxmone-h3-max-videos")
	for _, tc := range []struct {
		name   string
		images []MediaReference
		first  any
		last   any
		refs   []any
	}{
		{
			name:   "unroled references stay references",
			images: []MediaReference{{URL: "https://cdn.example/a.png", Order: 1}, {URL: "https://cdn.example/b.png", Order: 2}},
			refs:   []any{"https://cdn.example/a.png", "https://cdn.example/b.png"},
		},
		{
			name: "explicit frames leave references",
			images: []MediaReference{
				{URL: "https://cdn.example/ref.png", Role: "reference_image", Order: 1},
				{URL: "https://cdn.example/last.png", Role: "last_frame", Order: 3},
				{URL: "https://cdn.example/first.png", Role: "first_frame", Order: 2},
			},
			first: "https://cdn.example/first.png",
			last:  "https://cdn.example/last.png",
			refs:  []any{"https://cdn.example/ref.png"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create, err := adapter.BuildCreate(context.Background(), RequestContext{Request: GenerationRequest{
				Model: "minimax-h3-c", Prompt: "test", Duration: 4, Resolution: "768p", AspectRatio: "9:16", Images: tc.images,
			}})
			if err != nil {
				t.Fatal(err)
			}
			body, _ := create.Body.(map[string]any)
			if body["first_frame"] != tc.first || body["last_frame"] != tc.last {
				t.Fatalf("frames = %#v / %#v", body["first_frame"], body["last_frame"])
			}
			refs, _ := body["reference_images"].([]any)
			if len(refs) != len(tc.refs) {
				t.Fatalf("references = %#v", body["reference_images"])
			}
			for index := range tc.refs {
				if refs[index] != tc.refs[index] {
					t.Fatalf("references = %#v", refs)
				}
			}
		})
	}
}
