//go:build darwin && cgo

package main

import "testing"

func TestCocoaWidgetIDsMatchGo(t *testing.T) {
	if err := cocoaWidgetIDsMatch(); err != nil {
		t.Fatal(err)
	}
}
