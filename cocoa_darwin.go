//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework UniformTypeIdentifiers

#import <Cocoa/Cocoa.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// Widget IDs mirror the Go widgetID constants (checked by cocoaWidgetIDsMatch).
enum {
	PC_W_PROVIDER = 0,
	PC_W_MODEL = 1,
	PC_W_LIST_MODELS = 2,
	PC_W_BASE_URL = 3,
	PC_W_API_KEY = 4,
	PC_W_PROMPT = 5,
	PC_W_TIMEOUT = 6,
	PC_W_EFFORT = 7,
	PC_W_MODE = 8,
	PC_W_RUN = 9,
	PC_W_STOP = 10,
	PC_W_EXPORT = 11,
	PC_W_CLEAR = 12,
	PC_W_HISTORY_REFRESH = 13,
	PC_W_HISTORY_CLEAR = 14,
	PC_W_CHECK0 = 15,
	PC_MAX_CHECKS = 16,
	PC_W_COUNT = PC_W_CHECK0 + PC_MAX_CHECKS,
};
enum { PC_EV_ACTION = 0, PC_EV_TEXT = 1 };
enum { PC_TABLE_RESULTS = 0, PC_TABLE_HISTORY = 1 };

// Implemented in Go (cocoa_export_darwin.go).
extern void goOnEvent(int widget, int kind);
extern int goTableRows(int table);
extern char *goTableCell(int table, int row, int col, int *style);
extern void goTableActivated(int table, int row);
extern void goWindowClosing(void);
extern void goRunHandle(uintptr_t handle);

@interface PCDelegate : NSObject <NSApplicationDelegate, NSWindowDelegate, NSTextFieldDelegate,
	NSComboBoxDelegate, NSTableViewDataSource, NSTableViewDelegate>
@end

static NSWindow *pcWindow;
static PCDelegate *pcDelegate;
static NSControl *pcWidgets[PC_W_COUNT];
static NSTableView *pcTables[2];
static NSTextView *pcLog;
static NSTextField *pcSummary;
static NSTextField *pcStatus;
static BOOL pcClosed;

static NSString *pcString(const char *s) {
	if (s == NULL) return @"";
	NSString *str = [NSString stringWithUTF8String:s];
	return str ? str : @"";
}

static char *pcCopyCString(NSString *s) {
	const char *u = s ? [s UTF8String] : "";
	return strdup(u ? u : "");
}

static NSControl *pcWidget(int widget) {
	if (widget < 0 || widget >= PC_W_COUNT) return nil;
	return pcWidgets[widget];
}

static void pcNotifyClosing(void) {
	if (pcClosed) return;
	pcClosed = YES;
	goWindowClosing();
}

// Cell styles mirror the Go cellStyle constants.
static void pcApplyCellStyle(NSTextField *f, int style) {
	CGFloat size = [NSFont systemFontSize];
	f.font = [NSFont systemFontOfSize:size];
	f.textColor = [NSColor labelColor];
	f.drawsBackground = NO;
	f.alignment = NSTextAlignmentLeft;
	NSColor *pill = nil;
	switch (style) {
	case 1: pill = [NSColor systemGreenColor]; break;
	case 2: pill = [NSColor systemRedColor]; break;
	case 3: pill = [NSColor systemGrayColor]; break;
	case 4: pill = [NSColor systemOrangeColor]; break;
	case 5: f.textColor = [NSColor systemGreenColor]; f.font = [NSFont boldSystemFontOfSize:size]; break;
	case 6: f.textColor = [NSColor systemRedColor]; f.font = [NSFont boldSystemFontOfSize:size]; break;
	case 7: f.textColor = [NSColor secondaryLabelColor]; f.font = [NSFont boldSystemFontOfSize:size]; break;
	default: break;
	}
	if (pill != nil) {
		f.drawsBackground = YES;
		f.backgroundColor = pill;
		f.textColor = [NSColor whiteColor];
		f.font = [NSFont boldSystemFontOfSize:size];
		f.alignment = NSTextAlignmentCenter;
	}
}

@implementation PCDelegate
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender { return YES; }
- (void)applicationWillTerminate:(NSNotification *)note { pcNotifyClosing(); }
- (void)windowWillClose:(NSNotification *)note { pcNotifyClosing(); }
- (void)onAction:(id)sender { goOnEvent((int)[sender tag], PC_EV_ACTION); }
- (void)controlTextDidChange:(NSNotification *)note {
	id obj = [note object];
	if ([obj isKindOfClass:[NSControl class]]) goOnEvent((int)[obj tag], PC_EV_TEXT);
}
- (NSInteger)numberOfRowsInTableView:(NSTableView *)tableView {
	return goTableRows((int)tableView.tag);
}
- (NSView *)tableView:(NSTableView *)tableView viewForTableColumn:(NSTableColumn *)column row:(NSInteger)row {
	NSTableCellView *cell = [tableView makeViewWithIdentifier:@"pcCell" owner:self];
	if (cell == nil) {
		cell = [[NSTableCellView alloc] initWithFrame:NSZeroRect];
		cell.identifier = @"pcCell";
		NSTextField *f = [NSTextField labelWithString:@""];
		f.translatesAutoresizingMaskIntoConstraints = NO;
		f.lineBreakMode = NSLineBreakByTruncatingTail;
		f.usesSingleLineMode = YES;
		f.maximumNumberOfLines = 1;
		f.wantsLayer = YES;
		f.layer.cornerRadius = 4;
		f.layer.masksToBounds = YES;
		[f setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
		[cell addSubview:f];
		cell.textField = f;
		[NSLayoutConstraint activateConstraints:@[
			[f.leadingAnchor constraintEqualToAnchor:cell.leadingAnchor constant:3],
			[f.trailingAnchor constraintEqualToAnchor:cell.trailingAnchor constant:-3],
			[f.centerYAnchor constraintEqualToAnchor:cell.centerYAnchor],
		]];
	}
	int style = 0;
	char *s = goTableCell((int)tableView.tag, (int)row, (int)[column.identifier integerValue], &style);
	NSString *text = pcString(s);
	free(s);
	cell.textField.stringValue = text;
	cell.toolTip = text.length > 0 ? text : nil;
	pcApplyCellStyle(cell.textField, style);
	return cell;
}
- (void)onTableDoubleClick:(id)sender {
	NSTableView *tv = sender;
	NSInteger row = tv.clickedRow;
	if (row >= 0) goTableActivated((int)tv.tag, (int)row);
}
@end

// ---- Construction helpers ----

static void pcPin(NSView *v, NSView *parent, CGFloat top, CGFloat left, CGFloat bottom, CGFloat right, BOOL fillWidth) {
	v.translatesAutoresizingMaskIntoConstraints = NO;
	[NSLayoutConstraint activateConstraints:@[
		[v.topAnchor constraintEqualToAnchor:parent.topAnchor constant:top],
		[v.leadingAnchor constraintEqualToAnchor:parent.leadingAnchor constant:left],
		[v.bottomAnchor constraintEqualToAnchor:parent.bottomAnchor constant:-bottom],
		fillWidth ? [v.trailingAnchor constraintEqualToAnchor:parent.trailingAnchor constant:-right]
		          : [v.trailingAnchor constraintLessThanOrEqualToAnchor:parent.trailingAnchor constant:-right],
	]];
}

static NSTextField *pcFieldLabel(NSString *text) {
	NSTextField *l = [NSTextField labelWithString:text];
	l.textColor = [NSColor secondaryLabelColor];
	l.alignment = NSTextAlignmentRight;
	[l setContentHuggingPriority:NSLayoutPriorityRequired forOrientation:NSLayoutConstraintOrientationHorizontal];
	return l;
}

static NSTextField *pcTextInput(int tag, NSString *placeholder, NSString *tip, BOOL secure) {
	NSTextField *f = secure ? [NSSecureTextField textFieldWithString:@""] : [NSTextField textFieldWithString:@""];
	f.placeholderString = placeholder;
	f.toolTip = tip;
	f.tag = tag;
	f.delegate = pcDelegate;
	f.usesSingleLineMode = YES;
	f.lineBreakMode = NSLineBreakByClipping;
	[f.cell setScrollable:YES];
	[f.cell setWraps:NO];
	[f setContentHuggingPriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
	[f.widthAnchor constraintGreaterThanOrEqualToConstant:220].active = YES;
	pcWidgets[tag] = f;
	return f;
}

static NSPopUpButton *pcPopup(int tag, NSString *tip) {
	NSPopUpButton *p = [[NSPopUpButton alloc] initWithFrame:NSZeroRect pullsDown:NO];
	p.tag = tag;
	p.toolTip = tip;
	p.target = pcDelegate;
	p.action = @selector(onAction:);
	[p setContentHuggingPriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
	pcWidgets[tag] = p;
	return p;
}

static NSButton *pcButton(int tag, NSString *title, NSString *tip) {
	NSButton *b = [NSButton buttonWithTitle:title target:pcDelegate action:@selector(onAction:)];
	b.tag = tag;
	b.toolTip = tip;
	pcWidgets[tag] = b;
	return b;
}

static NSStackView *pcHStack(NSArray<NSView *> *views, CGFloat spacing) {
	NSStackView *s = [NSStackView stackViewWithViews:views];
	s.orientation = NSUserInterfaceLayoutOrientationHorizontal;
	s.alignment = NSLayoutAttributeCenterY;
	s.spacing = spacing;
	s.translatesAutoresizingMaskIntoConstraints = NO;
	return s;
}

static NSBox *pcGroup(NSString *title, NSView *content, BOOL fillWidth) {
	NSBox *box = [[NSBox alloc] init];
	box.title = title;
	box.titleFont = [NSFont boldSystemFontOfSize:[NSFont systemFontSize]];
	box.titlePosition = NSAtTop;
	box.translatesAutoresizingMaskIntoConstraints = NO;
	[box.contentView addSubview:content];
	pcPin(content, box.contentView, 10, 12, 10, 12, fillWidth);
	return box;
}

static NSView *pcInset(NSView *content, CGFloat inset) {
	NSView *v = [[NSView alloc] initWithFrame:NSZeroRect];
	[v addSubview:content];
	pcPin(content, v, inset, inset, inset, inset, YES);
	return v;
}

static NSScrollView *pcMakeTable(int tag, NSArray<NSString *> *titles, NSArray<NSNumber *> *widths, NSInteger centered) {
	NSTableView *tv = [[NSTableView alloc] initWithFrame:NSZeroRect];
	tv.tag = tag;
	tv.usesAlternatingRowBackgroundColors = YES;
	tv.allowsColumnReordering = YES;
	tv.allowsColumnResizing = YES;
	tv.allowsMultipleSelection = NO;
	tv.columnAutoresizingStyle = NSTableViewLastColumnOnlyAutoresizingStyle;
	tv.rowHeight = 24;
	tv.style = NSTableViewStyleFullWidth;
	tv.gridStyleMask = NSTableViewSolidVerticalGridLineMask;
	for (NSUInteger i = 0; i < titles.count; i++) {
		NSTableColumn *c = [[NSTableColumn alloc] initWithIdentifier:[NSString stringWithFormat:@"%lu", (unsigned long)i]];
		c.title = titles[i];
		c.width = widths[i].doubleValue;
		c.minWidth = 50;
		c.resizingMask = NSTableColumnUserResizingMask | NSTableColumnAutoresizingMask;
		if ((NSInteger)i == centered) c.headerCell.alignment = NSTextAlignmentCenter;
		[tv addTableColumn:c];
	}
	tv.dataSource = pcDelegate;
	tv.delegate = pcDelegate;
	tv.target = pcDelegate;
	tv.doubleAction = @selector(onTableDoubleClick:);
	pcTables[tag] = tv;

	NSScrollView *sv = [[NSScrollView alloc] initWithFrame:NSZeroRect];
	sv.documentView = tv;
	sv.hasVerticalScroller = YES;
	sv.hasHorizontalScroller = YES;
	sv.autohidesScrollers = YES;
	sv.borderType = NSBezelBorder;
	return sv;
}

static void pcAddMenuItem(NSMenu *menu, NSString *title, SEL action, NSString *key, NSEventModifierFlags mods) {
	NSMenuItem *item = [menu addItemWithTitle:title action:action keyEquivalent:key];
	if (mods != 0) item.keyEquivalentModifierMask = mods;
}

static void pcBuildMenu(NSString *appName) {
	NSMenu *bar = [[NSMenu alloc] init];

	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	[bar addItem:appItem];
	NSMenu *appMenu = [[NSMenu alloc] initWithTitle:appName];
	pcAddMenuItem(appMenu, [@"About " stringByAppendingString:appName], @selector(orderFrontStandardAboutPanel:), @"", 0);
	[appMenu addItem:[NSMenuItem separatorItem]];
	pcAddMenuItem(appMenu, [@"Hide " stringByAppendingString:appName], @selector(hide:), @"h", 0);
	pcAddMenuItem(appMenu, @"Hide Others", @selector(hideOtherApplications:), @"h", NSEventModifierFlagCommand | NSEventModifierFlagOption);
	pcAddMenuItem(appMenu, @"Show All", @selector(unhideAllApplications:), @"", 0);
	[appMenu addItem:[NSMenuItem separatorItem]];
	pcAddMenuItem(appMenu, [@"Quit " stringByAppendingString:appName], @selector(terminate:), @"q", 0);
	appItem.submenu = appMenu;

	// The Edit menu is what makes Cmd+C / Cmd+V / Cmd+A work in text fields.
	NSMenuItem *editItem = [[NSMenuItem alloc] init];
	[bar addItem:editItem];
	NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Edit"];
	pcAddMenuItem(editMenu, @"Undo", @selector(undo:), @"z", 0);
	pcAddMenuItem(editMenu, @"Redo", @selector(redo:), @"z", NSEventModifierFlagCommand | NSEventModifierFlagShift);
	[editMenu addItem:[NSMenuItem separatorItem]];
	pcAddMenuItem(editMenu, @"Cut", @selector(cut:), @"x", 0);
	pcAddMenuItem(editMenu, @"Copy", @selector(copy:), @"c", 0);
	pcAddMenuItem(editMenu, @"Paste", @selector(paste:), @"v", 0);
	pcAddMenuItem(editMenu, @"Select All", @selector(selectAll:), @"a", 0);
	editItem.submenu = editMenu;

	NSMenuItem *windowItem = [[NSMenuItem alloc] init];
	[bar addItem:windowItem];
	NSMenu *windowMenu = [[NSMenu alloc] initWithTitle:@"Window"];
	pcAddMenuItem(windowMenu, @"Minimize", @selector(performMiniaturize:), @"m", 0);
	pcAddMenuItem(windowMenu, @"Zoom", @selector(performZoom:), @"", 0);
	[windowMenu addItem:[NSMenuItem separatorItem]];
	pcAddMenuItem(windowMenu, @"Close", @selector(performClose:), @"w", 0);
	windowItem.submenu = windowMenu;

	[NSApp setMainMenu:bar];
	[NSApp setWindowsMenu:windowMenu];
}

// ---- Entry points called from Go ----

void pc_app_create(const char **features, int featureCount) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
		pcDelegate = [[PCDelegate alloc] init];
		[NSApp setDelegate:pcDelegate];
		pcBuildMenu(@"Provider Checker");

		pcWindow = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 1040, 800)
			styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
			backing:NSBackingStoreBuffered defer:NO];
		pcWindow.title = @"LLM Provider Checker";
		pcWindow.releasedWhenClosed = NO;
		pcWindow.delegate = pcDelegate;
		pcWindow.contentMinSize = NSMakeSize(900, 700);
		[pcWindow center];
		[pcWindow setFrameAutosaveName:@"ProviderCheckerMainWindow"];

		// ---- Header ----
		NSTextField *title = [NSTextField labelWithString:@"LLM Provider Checker"];
		title.font = [NSFont boldSystemFontOfSize:18];
		NSTextField *subtitle = [NSTextField labelWithString:@"一键检测大模型服务可用性与能力"];
		subtitle.textColor = [NSColor secondaryLabelColor];
		NSStackView *header = pcHStack(@[title, subtitle], 10);
		header.alignment = NSLayoutAttributeFirstBaseline;

		// ---- Provider settings ----
		NSPopUpButton *provider = pcPopup(PC_W_PROVIDER, nil);
		NSComboBox *model = [[NSComboBox alloc] initWithFrame:NSZeroRect];
		model.tag = PC_W_MODEL;
		model.delegate = pcDelegate;
		model.completes = NO;
		model.usesDataSource = NO;
		model.numberOfVisibleItems = 14;
		model.placeholderString = @"model name";
		model.toolTip = @"输入模型名称，或点击“列出模型”从服务商拉取可用模型后下拉选择。";
		[model setContentHuggingPriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
		pcWidgets[PC_W_MODEL] = model;
		NSButton *list = pcButton(PC_W_LIST_MODELS, @"列出模型 / List", @"从服务商 /models 接口拉取可用模型列表");
		[list setContentHuggingPriority:NSLayoutPriorityRequired forOrientation:NSLayoutConstraintOrientationHorizontal];
		NSStackView *modelRow = pcHStack(@[model, list], 6);

		NSTextField *baseURL = pcTextInput(PC_W_BASE_URL, @"https://api.openai.com  (empty = default)", nil, NO);
		NSTextField *apiKey = pcTextInput(PC_W_API_KEY, @"sk-... / token...",
			@"Anthropic 同时支持 API Key (x-api-key) 与 Auth Token (Bearer，401 时自动重试)。", YES);
		NSTextField *prompt = pcTextInput(PC_W_PROMPT, @"Say hello in one short sentence.", nil, NO);
		NSTextField *timeout = pcTextInput(PC_W_TIMEOUT, @"60", nil, NO);
		NSPopUpButton *effort = pcPopup(PC_W_EFFORT,
			@"OpenAI reasoning.effort（none/low/medium/high/xhigh/max）。留空时不发送该参数，实际默认由模型/服务商决定；显式选择（none 除外）时会省略探针的隐式 temperature。");
		NSPopUpButton *mode = pcPopup(PC_W_MODE, @"Responses API reasoning.mode。选择 pro 启用专业模式（保持所选模型别名不变）。");

		NSGridView *form = [NSGridView gridViewWithViews:@[
			@[pcFieldLabel(@"Type / 类型"), provider, pcFieldLabel(@"Model"), modelRow],
			@[pcFieldLabel(@"Base URL"), baseURL, pcFieldLabel(@"API Key / Auth Token"), apiKey],
			@[pcFieldLabel(@"Prompt (optional)"), prompt, pcFieldLabel(@"Timeout (s)"), timeout],
			@[pcFieldLabel(@"Reasoning Effort / 推理强度"), effort, pcFieldLabel(@"Reasoning Mode / 推理模式"), mode],
		]];
		form.rowSpacing = 8;
		form.columnSpacing = 8;
		form.yPlacement = NSGridCellPlacementCenter;
		[form columnAtIndex:0].xPlacement = NSGridCellPlacementTrailing;
		[form columnAtIndex:1].xPlacement = NSGridCellPlacementFill;
		[form columnAtIndex:2].xPlacement = NSGridCellPlacementTrailing;
		[form columnAtIndex:3].xPlacement = NSGridCellPlacementFill;
		// Label columns hug their text; the two input columns share the rest equally.
		for (NSInteger col = 0; col <= 2; col += 2) {
			CGFloat w = 0;
			for (NSInteger r = 0; r < form.numberOfRows; r++) {
				NSView *label = [form cellAtColumnIndex:col rowIndex:r].contentView;
				w = MAX(w, label.fittingSize.width);
			}
			[form columnAtIndex:col].width = ceil(w);
		}
		[baseURL.widthAnchor constraintEqualToAnchor:apiKey.widthAnchor].active = YES;
		NSBox *settings = pcGroup(@"Provider Settings  /  提供商设置", form, YES);

		// ---- Capabilities ----
		NSMutableArray *rows = [NSMutableArray array];
		int count = featureCount < PC_MAX_CHECKS ? featureCount : PC_MAX_CHECKS;
		for (int i = 0; i < count; i += 4) {
			NSMutableArray *row = [NSMutableArray array];
			for (int j = i; j < i + 4; j++) {
				if (j < count) {
					NSButton *cb = [NSButton checkboxWithTitle:pcString(features[j]) target:pcDelegate action:@selector(onAction:)];
					cb.tag = PC_W_CHECK0 + j;
					pcWidgets[PC_W_CHECK0 + j] = cb;
					[row addObject:cb];
				} else {
					[row addObject:[NSGridCell emptyContentView]];
				}
			}
			[rows addObject:row];
		}
		NSGridView *checks = [NSGridView gridViewWithViews:rows];
		checks.rowSpacing = 8;
		checks.columnSpacing = 28;
		NSBox *caps = pcGroup(@"Capabilities to Check  /  待检测能力 (multi-select)", checks, NO);

		// ---- Actions ----
		// Cmd+R rather than Return: Return belongs to the text fields and the
		// model combo box (choosing a listed model must not start a run).
		NSButton *run = pcButton(PC_W_RUN, @"▶  Run Check", @"Run the selected capability checks (⌘R)");
		run.keyEquivalent = @"r";
		run.keyEquivalentModifierMask = NSEventModifierFlagCommand;
		NSButton *stop = pcButton(PC_W_STOP, @"■  Stop", @"Stop the running check (⌘.)");
		stop.keyEquivalent = @".";
		stop.keyEquivalentModifierMask = NSEventModifierFlagCommand;
		stop.enabled = NO;
		NSButton *exportCSV = pcButton(PC_W_EXPORT, @"Export CSV…", @"Export the visible results table as CSV");
		NSButton *clear = pcButton(PC_W_CLEAR, @"Clear display",
			@"Clears the visible table/log/summary; run records and history counts retain all results. CSV exports the visible table.");
		for (NSButton *b in @[run, stop, exportCSV, clear]) {
			b.controlSize = NSControlSizeLarge;
			[b.widthAnchor constraintGreaterThanOrEqualToConstant:110].active = YES;
		}
		NSStackView *actions = pcHStack(@[run, stop, exportCSV, clear], 8);

		// ---- Summary strip ----
		NSBox *summaryBox = [[NSBox alloc] init];
		summaryBox.boxType = NSBoxCustom;
		summaryBox.titlePosition = NSNoTitle;
		summaryBox.borderWidth = 0;
		summaryBox.cornerRadius = 8;
		summaryBox.fillColor = [[NSColor controlAccentColor] colorWithAlphaComponent:0.10];
		summaryBox.contentViewMargins = NSMakeSize(0, 0);
		summaryBox.translatesAutoresizingMaskIntoConstraints = NO;
		pcSummary = [NSTextField labelWithString:@""];
		pcSummary.font = [NSFont boldSystemFontOfSize:14];
		pcSummary.lineBreakMode = NSLineBreakByTruncatingTail;
		[pcSummary setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
		[summaryBox.contentView addSubview:pcSummary];
		pcPin(pcSummary, summaryBox.contentView, 9, 14, 9, 14, NO);

		// ---- Tabs ----
		NSTabView *tabs = [[NSTabView alloc] initWithFrame:NSZeroRect];
		tabs.translatesAutoresizingMaskIntoConstraints = NO;

		NSScrollView *results = pcMakeTable(PC_TABLE_RESULTS,
			@[@"Feature", @"Status", @"Latency", @"Upstream Model / 上游响应模型", @"Detail", @"Error"],
			@[@180, @80, @80, @220, @300, @260], 1);
		NSTabViewItem *resultsItem = [[NSTabViewItem alloc] initWithIdentifier:@"results"];
		resultsItem.label = @"Results / 结果";
		resultsItem.view = pcInset(results, 8);
		[tabs addTabViewItem:resultsItem];

		NSScrollView *logScroll = [NSTextView scrollableTextView];
		pcLog = (NSTextView *)logScroll.documentView;
		pcLog.editable = NO;
		pcLog.selectable = YES;
		pcLog.richText = NO;
		pcLog.font = [NSFont monospacedSystemFontOfSize:11 weight:NSFontWeightRegular];
		pcLog.textContainerInset = NSMakeSize(4, 4);
		logScroll.borderType = NSBezelBorder;
		NSTabViewItem *logItem = [[NSTabViewItem alloc] initWithIdentifier:@"log"];
		logItem.label = @"Log / 日志";
		logItem.view = pcInset(logScroll, 8);
		[tabs addTabViewItem:logItem];

		NSTextField *hint = [NSTextField labelWithString:@"双击历史记录可回填该次测试配置 / Double-click a row to load its config"];
		hint.textColor = [NSColor secondaryLabelColor];
		hint.lineBreakMode = NSLineBreakByTruncatingTail;
		[hint setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
		NSStackView *historyBar = pcHStack(@[hint], 8);
		[historyBar addView:pcButton(PC_W_HISTORY_REFRESH, @"Refresh / 刷新", nil) inGravity:NSStackViewGravityTrailing];
		[historyBar addView:pcButton(PC_W_HISTORY_CLEAR, @"Clear History / 清空", nil) inGravity:NSStackViewGravityTrailing];
		NSScrollView *historyTable = pcMakeTable(PC_TABLE_HISTORY,
			@[@"Time", @"Provider", @"Model", @"Features", @"P/F/S", @"Reasoning"],
			@[@150, @190, @180, @240, @70, @130], 4);
		NSStackView *historyPane = [NSStackView stackViewWithViews:@[historyBar, historyTable]];
		historyPane.orientation = NSUserInterfaceLayoutOrientationVertical;
		historyPane.alignment = NSLayoutAttributeLeading;
		historyPane.spacing = 6;
		historyPane.distribution = NSStackViewDistributionFill;
		[historyBar.widthAnchor constraintEqualToAnchor:historyPane.widthAnchor].active = YES;
		[historyTable.widthAnchor constraintEqualToAnchor:historyPane.widthAnchor].active = YES;
		[historyTable setContentHuggingPriority:1 forOrientation:NSLayoutConstraintOrientationVertical];
		NSTabViewItem *historyItem = [[NSTabViewItem alloc] initWithIdentifier:@"history"];
		historyItem.label = @"History / 历史";
		historyItem.view = pcInset(historyPane, 8);
		[tabs addTabViewItem:historyItem];

		// ---- Status line ----
		pcStatus = [NSTextField labelWithString:@"Ready."];
		pcStatus.font = [NSFont systemFontOfSize:[NSFont smallSystemFontSize]];
		pcStatus.textColor = [NSColor secondaryLabelColor];

		// ---- Root layout ----
		NSArray<NSView *> *sections = @[header, settings, caps, actions, summaryBox, tabs, pcStatus];
		NSStackView *root = [NSStackView stackViewWithViews:sections];
		root.orientation = NSUserInterfaceLayoutOrientationVertical;
		root.alignment = NSLayoutAttributeLeading;
		root.spacing = 10;
		root.distribution = NSStackViewDistributionFill;
		root.edgeInsets = NSEdgeInsetsMake(14, 16, 10, 16);
		NSView *content = pcWindow.contentView;
		[content addSubview:root];
		pcPin(root, content, 0, 0, 0, 0, YES);
		for (NSView *v in @[settings, caps, summaryBox, tabs]) {
			[v.widthAnchor constraintEqualToAnchor:root.widthAnchor constant:-32].active = YES;
		}
		for (NSView *v in sections) {
			[v setContentHuggingPriority:NSLayoutPriorityDefaultHigh forOrientation:NSLayoutConstraintOrientationVertical];
		}
		[tabs setContentHuggingPriority:1 forOrientation:NSLayoutConstraintOrientationVertical];
		[tabs.heightAnchor constraintGreaterThanOrEqualToConstant:240].active = YES;

		pcWindow.initialFirstResponder = baseURL;
		pcWindow.autorecalculatesKeyViewLoop = YES;
	}
}

void pc_app_run(void) {
	[pcWindow makeKeyAndOrderFront:nil];
	if (@available(macOS 14.0, *)) {
		[NSApp activate];
	} else {
		[NSApp activateIgnoringOtherApps:YES];
	}
	[NSApp run];
}

char *pc_get_text(int widget) {
	NSControl *c = pcWidget(widget);
	return pcCopyCString(c ? c.stringValue : @"");
}

void pc_set_text(int widget, const char *text) {
	NSControl *c = pcWidget(widget);
	if (c) c.stringValue = pcString(text);
}

int pc_get_enabled(int widget) {
	NSControl *c = pcWidget(widget);
	return c && c.enabled ? 1 : 0;
}

void pc_set_enabled(int widget, int on) {
	NSControl *c = pcWidget(widget);
	if (c) c.enabled = on ? YES : NO;
}

int pc_get_checked(int widget) {
	NSControl *c = pcWidget(widget);
	if (![c isKindOfClass:[NSButton class]]) return 0;
	return ((NSButton *)c).state == NSControlStateValueOn ? 1 : 0;
}

void pc_set_checked(int widget, int on) {
	NSControl *c = pcWidget(widget);
	if ([c isKindOfClass:[NSButton class]]) ((NSButton *)c).state = on ? NSControlStateValueOn : NSControlStateValueOff;
}

int pc_get_index(int widget) {
	NSControl *c = pcWidget(widget);
	if ([c isKindOfClass:[NSPopUpButton class]]) return (int)((NSPopUpButton *)c).indexOfSelectedItem;
	return -1;
}

void pc_set_index(int widget, int index) {
	NSControl *c = pcWidget(widget);
	if (![c isKindOfClass:[NSPopUpButton class]]) return;
	NSPopUpButton *p = (NSPopUpButton *)c;
	if (index >= 0 && index < p.numberOfItems) [p selectItemAtIndex:index];
}

void pc_set_items(int widget, const char **items, int count) {
	NSControl *c = pcWidget(widget);
	if ([c isKindOfClass:[NSPopUpButton class]]) {
		NSPopUpButton *p = (NSPopUpButton *)c;
		[p removeAllItems];
		for (int i = 0; i < count; i++) [p addItemWithTitle:pcString(items[i])];
	} else if ([c isKindOfClass:[NSComboBox class]]) {
		NSComboBox *cb = (NSComboBox *)c;
		NSString *current = [cb.stringValue copy];
		[cb removeAllItems];
		for (int i = 0; i < count; i++) [cb addItemWithObjectValue:pcString(items[i])];
		cb.stringValue = current;
	}
}

void pc_table_reload(int table) {
	if (table >= 0 && table < 2) [pcTables[table] reloadData];
}

void pc_log_append(const char *line) {
	NSTextStorage *storage = pcLog.textStorage;
	NSString *text = pcString(line);
	if (storage.length > 0) text = [@"\n" stringByAppendingString:text];
	NSDictionary *attrs = @{
		NSFontAttributeName: [NSFont monospacedSystemFontOfSize:11 weight:NSFontWeightRegular],
		NSForegroundColorAttributeName: [NSColor textColor],
	};
	[storage appendAttributedString:[[NSAttributedString alloc] initWithString:text attributes:attrs]];
	[pcLog scrollRangeToVisible:NSMakeRange(storage.length, 0)];
}

void pc_log_clear(void) {
	[pcLog.textStorage setAttributedString:[[NSAttributedString alloc] initWithString:@""]];
}

// Tones mirror the Go tone constants.
void pc_set_summary(const char *text, int tone) {
	pcSummary.stringValue = pcString(text);
	switch (tone) {
	case 1: pcSummary.textColor = [NSColor systemGreenColor]; break;
	case 2: pcSummary.textColor = [NSColor systemRedColor]; break;
	case 3: pcSummary.textColor = [NSColor systemOrangeColor]; break;
	default: pcSummary.textColor = [NSColor secondaryLabelColor]; break;
	}
}

void pc_set_status(const char *text) {
	pcStatus.stringValue = pcString(text);
}

// pc_alert shows a modal alert; with confirm it offers Yes/No and returns 1 for Yes.
int pc_alert(const char *title, const char *message, int style, int confirm) {
	NSAlert *alert = [[NSAlert alloc] init];
	alert.messageText = pcString(title);
	alert.informativeText = pcString(message);
	switch (style) {
	case 1: alert.alertStyle = NSAlertStyleWarning; break;
	case 2: alert.alertStyle = NSAlertStyleCritical; break;
	default: alert.alertStyle = NSAlertStyleInformational; break;
	}
	if (confirm) {
		[alert addButtonWithTitle:@"Yes / 是"];
		[alert addButtonWithTitle:@"No / 否"];
	} else {
		[alert addButtonWithTitle:@"OK"];
	}
	return [alert runModal] == NSAlertFirstButtonReturn ? 1 : 0;
}

// pc_save_panel returns a malloc'd path, or NULL when cancelled.
char *pc_save_panel(const char *title, const char *name) {
	NSSavePanel *panel = [NSSavePanel savePanel];
	panel.title = pcString(title);
	panel.nameFieldStringValue = pcString(name);
	panel.canCreateDirectories = YES;
	UTType *csv = [UTType typeWithFilenameExtension:@"csv"];
	if (csv != nil) panel.allowedContentTypes = @[csv];
	panel.allowsOtherFileTypes = YES;
	if ([panel runModal] != NSModalResponseOK || panel.URL == nil) return NULL;
	return pcCopyCString(panel.URL.path);
}

void pc_dispatch(uintptr_t handle) {
	dispatch_async(dispatch_get_main_queue(), ^{ goRunHandle(handle); });
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"runtime/cgo"
	"unsafe"

	"provider-checker/checker"
)

// AppKit must run on the process's main thread: keep the main goroutine there.
func init() { runtime.LockOSThread() }

// theApp is the controller the exported Cocoa callbacks talk to.
var theApp *macApp

func main() {
	if err := cocoaWidgetIDsMatch(); err != nil {
		panic(err)
	}
	names := make([]string, len(checker.UnifiedFeatures))
	for i, f := range checker.UnifiedFeatures {
		names[i] = f.Name
	}
	if len(names) > maxChecks {
		panic(fmt.Sprintf("%d capabilities exceed the %d checkbox slots", len(names), maxChecks))
	}
	arr, free := cStringArray(names)
	C.pc_app_create(arr, C.int(len(names)))
	free()

	theApp = newMacApp(cocoaView{}, newAppState(), encryptSecret, decryptSecret)
	theApp.start()
	C.pc_app_run() // never returns; the app exits through NSApp terminate
}

// cocoaWidgetIDsMatch verifies the Go widget/table IDs agree with the Cocoa enums.
func cocoaWidgetIDsMatch() error {
	pairs := []struct {
		name string
		goID int
		cID  int
	}{
		{"provider", int(wProvider), int(C.PC_W_PROVIDER)},
		{"model", int(wModel), int(C.PC_W_MODEL)},
		{"list models", int(wListModels), int(C.PC_W_LIST_MODELS)},
		{"base URL", int(wBaseURL), int(C.PC_W_BASE_URL)},
		{"API key", int(wAPIKey), int(C.PC_W_API_KEY)},
		{"prompt", int(wPrompt), int(C.PC_W_PROMPT)},
		{"timeout", int(wTimeout), int(C.PC_W_TIMEOUT)},
		{"effort", int(wEffort), int(C.PC_W_EFFORT)},
		{"mode", int(wMode), int(C.PC_W_MODE)},
		{"run", int(wRun), int(C.PC_W_RUN)},
		{"stop", int(wStop), int(C.PC_W_STOP)},
		{"export", int(wExport), int(C.PC_W_EXPORT)},
		{"clear", int(wClear), int(C.PC_W_CLEAR)},
		{"history refresh", int(wHistoryRefresh), int(C.PC_W_HISTORY_REFRESH)},
		{"history clear", int(wHistoryClear), int(C.PC_W_HISTORY_CLEAR)},
		{"first checkbox", int(wCheck0), int(C.PC_W_CHECK0)},
		{"checkbox slots", maxChecks, int(C.PC_MAX_CHECKS)},
		{"widget count", int(wCount), int(C.PC_W_COUNT)},
		{"results table", int(tableResults), int(C.PC_TABLE_RESULTS)},
		{"history table", int(tableHistory), int(C.PC_TABLE_HISTORY)},
		{"action event", int(evAction), int(C.PC_EV_ACTION)},
		{"text event", int(evTextChanged), int(C.PC_EV_TEXT)},
	}
	for _, p := range pairs {
		if p.goID != p.cID {
			return fmt.Errorf("cocoa id mismatch for %s: Go %d, Cocoa %d", p.name, p.goID, p.cID)
		}
	}
	return nil
}

// cStringArray copies strs into a malloc'd char* array; call free when done.
func cStringArray(strs []string) (**C.char, func()) {
	n := len(strs)
	if n == 0 {
		return nil, func() {}
	}
	arr := (**C.char)(C.malloc(C.size_t(n) * C.size_t(unsafe.Sizeof(uintptr(0)))))
	items := unsafe.Slice(arr, n)
	for i, s := range strs {
		items[i] = C.CString(s)
	}
	return arr, func() {
		for _, p := range items {
			C.free(unsafe.Pointer(p))
		}
		C.free(unsafe.Pointer(arr))
	}
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// cocoaView implements macView on top of the AppKit widgets built by
// pc_app_create. All methods except Synchronize must run on the main thread.
type cocoaView struct{}

func (cocoaView) Text(id widgetID) string {
	p := C.pc_get_text(C.int(id))
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

func (cocoaView) SetText(id widgetID, s string) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	C.pc_set_text(C.int(id), cs)
}

func (cocoaView) Enabled(id widgetID) bool        { return C.pc_get_enabled(C.int(id)) != 0 }
func (cocoaView) SetEnabled(id widgetID, on bool) { C.pc_set_enabled(C.int(id), boolInt(on)) }
func (cocoaView) Checked(id widgetID) bool        { return C.pc_get_checked(C.int(id)) != 0 }
func (cocoaView) SetChecked(id widgetID, on bool) { C.pc_set_checked(C.int(id), boolInt(on)) }
func (cocoaView) SelectedIndex(id widgetID) int   { return int(C.pc_get_index(C.int(id))) }
func (cocoaView) SelectIndex(id widgetID, i int)  { C.pc_set_index(C.int(id), C.int(i)) }

func (cocoaView) SetItems(id widgetID, items []string) {
	arr, free := cStringArray(items)
	defer free()
	C.pc_set_items(C.int(id), arr, C.int(len(items)))
}

func (cocoaView) ReloadTable(t tableID) { C.pc_table_reload(C.int(t)) }

func (cocoaView) AppendLog(line string) {
	cs := C.CString(line)
	defer C.free(unsafe.Pointer(cs))
	C.pc_log_append(cs)
}

func (cocoaView) ClearLog() { C.pc_log_clear() }

func (cocoaView) SetSummary(text string, t tone) {
	cs := C.CString(text)
	defer C.free(unsafe.Pointer(cs))
	C.pc_set_summary(cs, C.int(t))
}

func (cocoaView) SetStatus(text string) {
	cs := C.CString(text)
	defer C.free(unsafe.Pointer(cs))
	C.pc_set_status(cs)
}

func showAlert(style alertStyle, title, message string, confirm bool) bool {
	ct, cm := C.CString(title), C.CString(message)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cm))
	return C.pc_alert(ct, cm, C.int(style), boolInt(confirm)) != 0
}

func (cocoaView) Alert(style alertStyle, title, message string) {
	showAlert(style, title, message, false)
}

func (cocoaView) Confirm(title, message string) bool {
	return showAlert(alertWarning, title, message, true)
}

func (cocoaView) SaveFile(title, defaultName string) (string, bool) {
	ct, cn := C.CString(title), C.CString(defaultName)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cn))
	p := C.pc_save_panel(ct, cn)
	if p == nil {
		return "", false
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p), true
}

func (cocoaView) Synchronize(fn func()) {
	C.pc_dispatch(C.uintptr_t(cgo.NewHandle(fn)))
}
