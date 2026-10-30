//go:build darwin && cgo

package main

/*
#include <stdint.h>
*/
import "C"

import "runtime/cgo"

// Callbacks invoked by the AppKit layer in cocoa_darwin.go, always on the main thread.

//export goOnEvent
func goOnEvent(widget, kind C.int) {
	if theApp != nil {
		theApp.handleEvent(widgetID(widget), eventKind(kind))
	}
}

//export goTableRows
func goTableRows(table C.int) C.int {
	if theApp == nil {
		return 0
	}
	return C.int(theApp.tableRows(tableID(table)))
}

// goTableCell returns a malloc'd C string (freed by the caller) and stores the
// cell style in *style.
//
//export goTableCell
func goTableCell(table, row, col C.int, style *C.int) *C.char {
	text, st := "", cellPlain
	if theApp != nil {
		text, st = theApp.tableCell(tableID(table), int(row), int(col))
	}
	*style = C.int(st)
	return C.CString(text)
}

//export goTableActivated
func goTableActivated(table, row C.int) {
	if theApp != nil && tableID(table) == tableHistory {
		theApp.loadHistoryRow(int(row))
	}
}

//export goWindowClosing
func goWindowClosing() {
	if theApp != nil {
		theApp.closing()
	}
}

//export goRunHandle
func goRunHandle(h C.uintptr_t) {
	handle := cgo.Handle(h)
	fn := handle.Value().(func())
	handle.Delete()
	fn()
}
