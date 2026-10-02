//go:build windows

package main

import (
	"encoding/csv"
	"errors"
	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"provider-checker/checker"
)

func TestWriteResultsCSVIncludesUpstreamResponseModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.csv")
	results := []checker.FeatureResult{{
		Name:                  "Basic",
		Status:                checker.StatusPass,
		UpstreamResponseModel: "gpt-5.2-2026-01-15",
		Detail:                "hello",
	}}
	if err := writeResultsCSV(path, results); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	if got := rows[0][3]; got != "UpstreamResponseModel" {
		t.Fatalf("header = %q, want UpstreamResponseModel", got)
	}
	if got := rows[1][3]; got != "gpt-5.2-2026-01-15" {
		t.Fatalf("upstream response model = %q", got)
	}
}

func TestCapabilityWidgetsSynchronizeSelection(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	st := &appState{providerKey: "openai-chat", features: map[string]bool{"basic": true}}
	var refs []*walk.CheckBox
	widgets := buildCheckWidgets(&refs, st)
	var mw *walk.MainWindow
	if err := (MainWindow{AssignTo: &mw, Layout: VBox{}, Children: widgets}).Create(); err != nil {
		t.Fatal(err)
	}
	defer mw.Dispose()
	for i, ref := range refs {
		if ref == nil {
			t.Fatalf("checkbox %d not assigned", i)
		}
	}
	refreshChecks(refs, st)
	syncChecks(refs, st)
	if !refs[0].Checked() || !st.features["basic"] {
		t.Fatal("default Basic mismatch")
	}
	st.features["json"] = true
	st.providerKey = "anthropic"
	refreshChecks(refs, st)
	syncChecks(refs, st)
	if refs[4].Enabled() || refs[4].Checked() || st.features["json"] {
		t.Fatal("unsupported JSON remains selected")
	}
	st.providerKey = "openai-chat"
	refreshChecks(refs, st)
	syncChecks(refs, st)
	if refs[4].Checked() {
		t.Fatal("JSON revived after provider switch")
	}
	st.features = map[string]bool{"tools": true}
	syncChecks(refs, st)
	if !refs[3].Checked() || refs[0].Checked() {
		t.Fatal("tools-only history mismatch")
	}
	// Programmatic visual changes do not stand in for an explicit state load.
	refs[0].SetChecked(true)
	if st.features["basic"] {
		t.Fatal("SetChecked unexpectedly changed internal selection")
	}
	// Dispatch the BN_CLICKED notification after the native check state changes.
	refs[0].WndProc(refs[0].Handle(), win.WM_COMMAND, uintptr(win.BN_CLICKED)<<16, uintptr(refs[0].Handle()))
	if !st.features["basic"] || !refs[0].Checked() {
		t.Fatal("button click did not update selection")
	}
}

func TestModelListingDeliveryKeepsLatestTextAndDropsStaleErrors(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	u := &uiRefs{modelModel: &stringListModel{}}
	if err := (MainWindow{AssignTo: &u.mw, Layout: VBox{}, Children: []Widget{
		ComboBox{AssignTo: &u.cbModel, Editable: true, Model: u.modelModel},
		PushButton{AssignTo: &u.btnModels, Text: "List"},
	}}).Create(); err != nil {
		t.Fatal(err)
	}
	defer u.mw.Dispose()
	st := &appState{providerKey: "openai-chat"}
	for _, latest := range []string{"edited-model", ""} {
		u.cbModel.SetText("request-model")
		generation := st.listing.begin(func() {})
		u.cbModel.SetText(latest)
		completeModelListing(u, st, generation, []string{"provider-model"}, nil)
		if u.cbModel.Text() != latest {
			t.Fatalf("latest %q overwritten by %q", latest, u.cbModel.Text())
		}
	}
	generation := st.listing.begin(func() {})
	st.providerKey = "anthropic"
	invalidateModels(u, st, true)
	u.cbModel.SetText("new-provider-model")
	completeModelListing(u, st, generation, []string{"old-provider-model"}, nil)
	completeModelListing(u, st, generation, nil, errors.New("stale error must not open dialog"))
	if u.cbModel.Text() != "new-provider-model" || len(u.modelModel.items) != 0 {
		t.Fatal("stale delivery changed form")
	}
	st.listing.close()
	completeModelListing(u, st, st.listing.generation, nil, errors.New("closed window"))
}
