package browser

import (
	"context"
	"errors"
	"testing"
)

type testViewer struct{}

func (testViewer) Snapshot(context.Context, int, int) (ViewSnapshot, error) {
	return ViewSnapshot{ContentType: "image/jpeg", Width: 320, Height: 180, Data: []byte("frame")}, nil
}

type sliceViewer []int

func (sliceViewer) Snapshot(context.Context, int, int) (ViewSnapshot, error) {
	return ViewSnapshot{ContentType: "image/jpeg", Width: 320, Height: 180, Data: []byte("slice")}, nil
}

func TestViewRegistryTracksOnlyActiveViewer(t *testing.T) {
	registry := NewViewRegistry()
	viewer := testViewer{}
	registry.Register("request-1", viewer)
	frame, err := registry.Snapshot(context.Background(), "request-1", 320, 180)
	if err != nil || string(frame.Data) != "frame" {
		t.Fatalf("snapshot = %#v, err=%v", frame, err)
	}
	registry.Unregister("request-1", viewer)
	if _, err := registry.Snapshot(context.Background(), "request-1", 320, 180); !errors.Is(err, ErrViewUnavailable) {
		t.Fatalf("snapshot after unregister = %v", err)
	}
}

func TestViewRegistryUnregistersNonComparableViewer(t *testing.T) {
	registry := NewViewRegistry()
	viewer := sliceViewer{1}
	registry.Register("request-slice", viewer)
	registry.Unregister("request-slice", viewer)
	if _, err := registry.Snapshot(context.Background(), "request-slice", 320, 180); !errors.Is(err, ErrViewUnavailable) {
		t.Fatalf("snapshot after unregister = %v", err)
	}
}
