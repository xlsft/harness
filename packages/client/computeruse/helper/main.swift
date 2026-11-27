// harness-computer-helper: the native half of the macOS computer use driver (../driver_darwin.go). It reports and acts;
// every decision is made by the client. It reads one JSON request per line on stdin and writes one JSON reply per line
// on stdout: {"ok":true,"result":...} or {"ok":false,"code":"...","error":"..."}.
//
// It never prompts for a permission: AXIsProcessTrusted and CGPreflightScreenCaptureAccess only read the state. Without
// Accessibility the input operations refuse, and without Screen Recording the window list simply has no titles.
//
// With --indicator it shows the panel "the agent is controlling this machine" with a Stop button instead: a click prints
// "stop" on stdout, each stdin line (a JSON string) replaces the label, and end of input ends the program.
import AppKit
import ApplicationServices
import Carbon.HIToolbox
import Foundation

// MARK: - Protocol

struct Refusal: Error {
    let code: String
    let message: String
    init(_ code: String, _ message: String) {
        self.code = code
        self.message = message
    }
}

func reply(_ object: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: object) else { return }
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write(Data([0x0A]))
}

/// Runs the run loop for a moment. NSWorkspace only learns about other apps through it, so waiting any other way would
/// leave the frontmost app stale.
func pump(_ seconds: TimeInterval) {
    RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: seconds))
}

// MARK: - Apps and windows

func appObject(_ app: NSRunningApplication?) -> [String: Any] {
    guard let app = app else { return ["id": "", "name": "", "running": false, "pid": 0] }
    return [
        "id": app.bundleIdentifier ?? "",
        "name": app.localizedName ?? "",
        "running": !app.isTerminated,
        "pid": Int(app.processIdentifier),
    ]
}

func runningApp(_ id: String) -> NSRunningApplication? {
    NSRunningApplication.runningApplications(withBundleIdentifier: id).first { !$0.isTerminated }
}

func frontBundleID() -> String {
    pump(0)
    return NSWorkspace.shared.frontmostApplication?.bundleIdentifier ?? ""
}

func listApps(installed: Bool) -> [[String: Any]] {
    var seen = Set<String>()
    var out: [[String: Any]] = []
    for app in NSWorkspace.shared.runningApplications where app.activationPolicy == .regular {
        guard let id = app.bundleIdentifier, seen.insert(id).inserted else { continue }
        out.append(appObject(app))
    }
    guard installed else { return out }
    var roots = ["/Applications", "/Applications/Utilities", "/System/Applications", "/System/Applications/Utilities"]
    roots.append(NSHomeDirectory() + "/Applications")
    for root in roots {
        guard let names = try? FileManager.default.contentsOfDirectory(atPath: root) else { continue }
        for name in names where name.hasSuffix(".app") {
            guard let bundle = Bundle(path: root + "/" + name), let id = bundle.bundleIdentifier, seen.insert(id).inserted else { continue }
            let display = bundle.object(forInfoDictionaryKey: "CFBundleDisplayName") as? String
                ?? bundle.object(forInfoDictionaryKey: "CFBundleName") as? String
                ?? String(name.dropLast(4))
            out.append(["id": id, "name": display, "running": false, "pid": 0])
        }
    }
    return out
}

func onScreenWindows() -> [[String: Any]] {
    let options: CGWindowListOption = [.optionOnScreenOnly, .excludeDesktopElements]
    return CGWindowListCopyWindowInfo(options, kCGNullWindowID) as? [[String: Any]] ?? []
}

func bounds(of info: [String: Any]) -> CGRect? {
    guard let dict = info[kCGWindowBounds as String] as? NSDictionary else { return nil }
    return CGRect(dictionaryRepresentation: dict as CFDictionary)
}

func windows(of id: String) -> [[String: Any]] {
    guard let app = runningApp(id) else { return [] }
    let pid = app.processIdentifier
    var out: [[String: Any]] = []
    for info in onScreenWindows() {
        guard let owner = info[kCGWindowOwnerPID as String] as? Int32, owner == pid,
              (info[kCGWindowLayer as String] as? Int ?? 0) == 0,
              (info[kCGWindowAlpha as String] as? Double ?? 1) > 0,
              let number = info[kCGWindowNumber as String] as? Int,
              let box = bounds(of: info), box.width > 1, box.height > 1 else { continue }
        out.append([
            "id": number,
            "title": info[kCGWindowName as String] as? String ?? "",
            "x": box.origin.x, "y": box.origin.y, "w": box.width, "h": box.height,
        ])
    }
    return out
}

/// The app of the top window at a point, on every layer: the menu bar and the Dock count as what they are.
func appAt(_ point: CGPoint) -> [String: Any] {
    for info in onScreenWindows() {
        guard (info[kCGWindowAlpha as String] as? Double ?? 1) > 0, let box = bounds(of: info), box.contains(point),
              let owner = info[kCGWindowOwnerPID as String] as? Int32 else { continue }
        return appObject(NSRunningApplication(processIdentifier: owner))
    }
    return appObject(nil)
}

// MARK: - Accessibility

func attribute(_ element: AXUIElement, _ name: String) -> AnyObject? {
    var value: AnyObject?
    guard AXUIElementCopyAttributeValue(element, name as CFString, &value) == .success else { return nil }
    return value
}

func string(_ element: AXUIElement, _ name: String) -> String {
    attribute(element, name) as? String ?? ""
}

func isSecure(_ element: AXUIElement) -> Bool {
    let role = string(element, kAXRoleAttribute)
    let subrole = string(element, kAXSubroleAttribute)
    return subrole == kAXSecureTextFieldSubrole || role == kAXSecureTextFieldSubrole
}

/// What has the keyboard focus. Secure input being on means a password field somewhere has asked macOS to hide keystrokes,
/// whatever the accessibility tree says.
func focused() -> [String: Any] {
    var out: [String: Any] = ["app": "", "role": "", "secure": IsSecureEventInputEnabled()]
    guard AXIsProcessTrusted() else { return out }
    let system = AXUIElementCreateSystemWide()
    if let raw = attribute(system, kAXFocusedApplicationAttribute), CFGetTypeID(raw) == AXUIElementGetTypeID() {
        var pid: pid_t = 0
        if AXUIElementGetPid(raw as! AXUIElement, &pid) == .success {
            out["app"] = NSRunningApplication(processIdentifier: pid)?.bundleIdentifier ?? ""
        }
    }
    if let raw = attribute(system, kAXFocusedUIElementAttribute), CFGetTypeID(raw) == AXUIElementGetTypeID() {
        let element = raw as! AXUIElement
        out["role"] = string(element, kAXRoleAttribute)
        if isSecure(element) { out["secure"] = true }
    }
    return out
}

func frame(of element: AXUIElement) -> CGRect? {
    guard let pos = attribute(element, kAXPositionAttribute), let size = attribute(element, kAXSizeAttribute),
          CFGetTypeID(pos) == AXValueGetTypeID(), CFGetTypeID(size) == AXValueGetTypeID() else { return nil }
    var p = CGPoint.zero
    var s = CGSize.zero
    guard AXValueGetValue(pos as! AXValue, .cgPoint, &p), AXValueGetValue(size as! AXValue, .cgSize, &s) else { return nil }
    return CGRect(origin: p, size: s)
}

func children(_ element: AXUIElement) -> [AXUIElement] {
    attribute(element, kAXChildrenAttribute) as? [AXUIElement] ?? []
}

/// The AX window matching a window of the window list, by its bounds: nothing public maps one to the other.
func axWindow(_ app: AXUIElement, windowID: Int, appID: String) -> AXUIElement? {
    let all = attribute(app, kAXWindowsAttribute) as? [AXUIElement] ?? []
    guard windowID != 0, let target = windows(of: appID).first(where: { $0["id"] as? Int == windowID }),
          let x = target["x"] as? CGFloat, let y = target["y"] as? CGFloat, let w = target["w"] as? CGFloat, let h = target["h"] as? CGFloat
    else { return all.first }
    let box = CGRect(x: x, y: y, width: w, height: h)
    return all.first { frame(of: $0) == box } ?? all.first
}

func find(appID: String, windowID: Int, query: String, role: String, max: Int) throws -> [[String: Any]] {
    guard let running = runningApp(appID) else { throw Refusal("not_running", "\(appID) is not running") }
    let app = AXUIElementCreateApplication(running.processIdentifier)
    AXUIElementSetMessagingTimeout(app, 1.5)
    guard let root = axWindow(app, windowID: windowID, appID: appID) else { return [] }
    let needle = query.lowercased()
    let wantRole = role.lowercased()
    var out: [[String: Any]] = []
    var queue: [(AXUIElement, Int)] = [(root, 0)]
    var visited = 0
    let deadline = Date(timeIntervalSinceNow: 8)
    while let (element, depth) = queue.first, out.count < max, visited < 4000, Date() < deadline {
        queue.removeFirst()
        visited += 1
        let elementRole = string(element, kAXRoleAttribute)
        let secure = isSecure(element)
        let title = string(element, kAXTitleAttribute)
        let description = string(element, kAXDescriptionAttribute)
        // A secure field's value is never read.
        let value = secure ? "" : (attribute(element, kAXValueAttribute).map { "\($0)" } ?? "")
        let haystack = [elementRole, title, description, value].joined(separator: "\n").lowercased()
        if (needle.isEmpty || haystack.contains(needle)) && (wantRole.isEmpty || elementRole.lowercased() == wantRole),
           let box = frame(of: element), box.width > 0, box.height > 0 {
            out.append([
                "role": elementRole, "title": title, "description": description, "value": String(value.prefix(500)),
                "x": box.origin.x, "y": box.origin.y, "w": box.width, "h": box.height,
                "enabled": attribute(element, kAXEnabledAttribute) as? Bool ?? true,
                "focused": attribute(element, kAXFocusedAttribute) as? Bool ?? false,
                "secure": secure,
            ])
        }
        if depth < 40 { queue.append(contentsOf: children(element).map { ($0, depth + 1) }) }
    }
    return out
}

/// The frames of the secure text fields in one window of an app, in screen points, for blanking them in a recording's
/// screenshots. With secure input on (a password field somewhere asked macOS to hide keystrokes) the focused element
/// counts as one too, whatever its role.
func secureFields(appID: String, windowID: Int) throws -> [String: Any] {
    guard let running = runningApp(appID) else { throw Refusal("not_running", "\(appID) is not running") }
    let app = AXUIElementCreateApplication(running.processIdentifier)
    AXUIElementSetMessagingTimeout(app, 1.0)
    var frames: [[String: Any]] = []
    func add(_ box: CGRect) {
        frames.append(["x": box.origin.x, "y": box.origin.y, "w": box.width, "h": box.height])
    }
    if let root = axWindow(app, windowID: windowID, appID: appID) {
        var queue: [(AXUIElement, Int)] = [(root, 0)]
        var visited = 0
        let deadline = Date(timeIntervalSinceNow: 3)
        while let (element, depth) = queue.first, visited < 4000, Date() < deadline {
            queue.removeFirst()
            visited += 1
            if isSecure(element), let box = frame(of: element) { add(box) }
            if depth < 40 { queue.append(contentsOf: children(element).map { ($0, depth + 1) }) }
        }
    }
    let secureInput = IsSecureEventInputEnabled()
    if secureInput, let raw = attribute(app, kAXFocusedUIElementAttribute), CFGetTypeID(raw) == AXUIElementGetTypeID(),
       let box = frame(of: raw as! AXUIElement) {
        add(box)
    }
    return ["frames": frames, "secure_input": secureInput]
}

func pressMenu(appID: String, path: [String]) throws {
    guard AXIsProcessTrusted() else { throw Refusal("no_permission", "Accessibility is not allowed") }
    guard let running = runningApp(appID) else { throw Refusal("not_running", "\(appID) is not running") }
    let app = AXUIElementCreateApplication(running.processIdentifier)
    guard let raw = attribute(app, kAXMenuBarAttribute), CFGetTypeID(raw) == AXUIElementGetTypeID() else {
        throw Refusal("no_menu", "\(appID) has no menu bar")
    }
    var current = raw as! AXUIElement
    for (index, title) in path.enumerated() {
        // A menu bar item's only child is its menu, whose children are the items.
        var items = children(current)
        if items.count == 1, string(items[0], kAXRoleAttribute) == kAXMenuRole { items = children(items[0]) }
        guard let item = items.first(where: { string($0, kAXTitleAttribute).caseInsensitiveCompare(title) == .orderedSame }) else {
            throw Refusal("no_menu", "no menu item \"\(title)\"")
        }
        if index == path.count - 1 {
            let result = AXUIElementPerformAction(item, kAXPressAction as CFString)
            guard result == .success else { throw Refusal("menu_failed", "the menu item did not respond") }
        } else {
            current = item
        }
    }
}

// MARK: - Activation

func activate(_ id: String) throws {
    if let app = runningApp(id) {
        if AXIsProcessTrusted() {
            AXUIElementSetAttributeValue(AXUIElementCreateApplication(app.processIdentifier), kAXFrontmostAttribute as CFString, kCFBooleanTrue)
        }
        app.activate(options: [.activateAllWindows])
    } else {
        guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: id) else {
            throw Refusal("not_found", "no app with id \(id)")
        }
        let config = NSWorkspace.OpenConfiguration()
        config.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: config) { _, _ in }
    }
    let deadline = Date(timeIntervalSinceNow: 5)
    while Date() < deadline {
        if frontBundleID() == id { return }
        pump(0.02)
    }
}

func focusWindow(appID: String, windowID: Int) throws {
    guard AXIsProcessTrusted() else { throw Refusal("no_permission", "Accessibility is not allowed") }
    guard let running = runningApp(appID) else { throw Refusal("not_running", "\(appID) is not running") }
    let app = AXUIElementCreateApplication(running.processIdentifier)
    guard let window = axWindow(app, windowID: windowID, appID: appID) else { throw Refusal("no_window", "no such window") }
    AXUIElementPerformAction(window, kAXRaiseAction as CFString)
    AXUIElementSetAttributeValue(window, kAXMainAttribute as CFString, kCFBooleanTrue)
}

// MARK: - Input

/// The last check before events are posted: the app the client expects is frontmost, and if asked no secure field has
/// the focus. Called again before each piece of a longer action.
func guardInput(expect: String, refuseSecure: Bool = false) throws {
    guard AXIsProcessTrusted() else { throw Refusal("no_permission", "Accessibility is not allowed") }
    if !expect.isEmpty && frontBundleID() != expect { throw Refusal("target_changed", "another app came to the front") }
    if refuseSecure && (focused()["secure"] as? Bool ?? true) { throw Refusal("secure_field", "a secure field has the focus") }
}

func flags(_ modifiers: [String]) -> CGEventFlags {
    var out: CGEventFlags = []
    for m in modifiers {
        switch m {
        case "cmd": out.insert(.maskCommand)
        case "ctrl": out.insert(.maskControl)
        case "alt": out.insert(.maskAlternate)
        case "shift": out.insert(.maskShift)
        default: break
        }
    }
    return out
}

func post(_ event: CGEvent?) throws {
    guard let event = event else { throw Refusal("event_failed", "could not create the event") }
    event.post(tap: .cghidEventTap)
}

func click(at point: CGPoint, right: Bool, count: Int, modifiers: [String], expect: String) throws {
    let down: CGEventType = right ? .rightMouseDown : .leftMouseDown
    let up: CGEventType = right ? .rightMouseUp : .leftMouseUp
    let button: CGMouseButton = right ? .right : .left
    try guardInput(expect: expect)
    try post(CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: point, mouseButton: .left))
    pump(0.03)
    for n in 1...max(1, count) {
        try guardInput(expect: expect)
        for type in [down, up] {
            let event = CGEvent(mouseEventSource: nil, mouseType: type, mouseCursorPosition: point, mouseButton: button)
            event?.setIntegerValueField(.mouseEventClickState, value: Int64(n))
            event?.flags = flags(modifiers)
            try post(event)
        }
        pump(0.03)
    }
}

func drag(from: CGPoint, to: CGPoint, expect: String) throws {
    try guardInput(expect: expect)
    try post(CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: from, mouseButton: .left))
    pump(0.03)
    try post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseDown, mouseCursorPosition: from, mouseButton: .left))
    pump(0.05)
    let steps = 12
    for i in 1...steps {
        if frontBundleID() != expect {
            // Let go at once, and say so.
            try post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseUp, mouseCursorPosition: from, mouseButton: .left))
            throw Refusal("target_changed", "another app came to the front")
        }
        let t = CGFloat(i) / CGFloat(steps)
        let point = CGPoint(x: from.x + (to.x - from.x) * t, y: from.y + (to.y - from.y) * t)
        try post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseDragged, mouseCursorPosition: point, mouseButton: .left))
        pump(0.015)
    }
    try post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseUp, mouseCursorPosition: to, mouseButton: .left))
}

/// Lets go of a mouse button left down by a helper killed in the middle of a click or drag.
func releaseMouse() throws {
    let at = CGEvent(source: nil)?.location ?? .zero
    if CGEventSource.buttonState(.combinedSessionState, button: .left) {
        try post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseUp, mouseCursorPosition: at, mouseButton: .left))
    }
    if CGEventSource.buttonState(.combinedSessionState, button: .right) {
        try post(CGEvent(mouseEventSource: nil, mouseType: .rightMouseUp, mouseCursorPosition: at, mouseButton: .right))
    }
}

func scroll(at point: CGPoint, dx: Int, dy: Int, expect: String) throws {
    try guardInput(expect: expect)
    try post(CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: point, mouseButton: .left))
    pump(0.02)
    // Positive means down and right for the caller; wheel events count the other way.
    let event = CGEvent(scrollWheelEvent2Source: nil, units: .line, wheelCount: 2, wheel1: Int32(-dy), wheel2: Int32(-dx), wheel3: 0)
    event?.location = point
    try post(event)
}

func typeText(_ text: String, expect: String, refuseSecure: Bool) throws {
    // In short pieces, checked one by one, so a change of focus stops the rest.
    var piece: [UInt16] = []
    func flush() throws {
        guard !piece.isEmpty else { return }
        try guardInput(expect: expect, refuseSecure: refuseSecure)
        for isDown in [true, false] {
            let event = CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: isDown)
            piece.withUnsafeBufferPointer { event?.keyboardSetUnicodeString(stringLength: $0.count, unicodeString: $0.baseAddress) }
            try post(event)
        }
        piece.removeAll()
        pump(0.008)
    }
    for ch in text {
        if ch == "\n" || ch == "\r\n" || ch == "\t" {
            try flush()
            try guardInput(expect: expect, refuseSecure: refuseSecure)
            try pressKey(code: ch == "\t" ? CGKeyCode(kVK_Tab) : CGKeyCode(kVK_Return), flags: [])
            pump(0.008)
            continue
        }
        let units = Array(String(ch).utf16)
        if piece.count + units.count > 16 { try flush() }
        piece.append(contentsOf: units)
    }
    try flush()
}

func pressKey(code: CGKeyCode, flags: CGEventFlags) throws {
    for isDown in [true, false] {
        let event = CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: isDown)
        event?.flags = flags
        try post(event)
    }
}

let keyCodes: [String: Int] = [
    "a": kVK_ANSI_A, "b": kVK_ANSI_B, "c": kVK_ANSI_C, "d": kVK_ANSI_D, "e": kVK_ANSI_E, "f": kVK_ANSI_F, "g": kVK_ANSI_G,
    "h": kVK_ANSI_H, "i": kVK_ANSI_I, "j": kVK_ANSI_J, "k": kVK_ANSI_K, "l": kVK_ANSI_L, "m": kVK_ANSI_M, "n": kVK_ANSI_N,
    "o": kVK_ANSI_O, "p": kVK_ANSI_P, "q": kVK_ANSI_Q, "r": kVK_ANSI_R, "s": kVK_ANSI_S, "t": kVK_ANSI_T, "u": kVK_ANSI_U,
    "v": kVK_ANSI_V, "w": kVK_ANSI_W, "x": kVK_ANSI_X, "y": kVK_ANSI_Y, "z": kVK_ANSI_Z,
    "0": kVK_ANSI_0, "1": kVK_ANSI_1, "2": kVK_ANSI_2, "3": kVK_ANSI_3, "4": kVK_ANSI_4, "5": kVK_ANSI_5, "6": kVK_ANSI_6,
    "7": kVK_ANSI_7, "8": kVK_ANSI_8, "9": kVK_ANSI_9,
    "-": kVK_ANSI_Minus, "=": kVK_ANSI_Equal, "[": kVK_ANSI_LeftBracket, "]": kVK_ANSI_RightBracket, "\\": kVK_ANSI_Backslash,
    ";": kVK_ANSI_Semicolon, "'": kVK_ANSI_Quote, ",": kVK_ANSI_Comma, ".": kVK_ANSI_Period, "/": kVK_ANSI_Slash, "`": kVK_ANSI_Grave,
    "enter": kVK_Return, "tab": kVK_Tab, "space": kVK_Space, "delete": kVK_Delete, "forward_delete": kVK_ForwardDelete,
    "escape": kVK_Escape, "up": kVK_UpArrow, "down": kVK_DownArrow, "left": kVK_LeftArrow, "right": kVK_RightArrow,
    "home": kVK_Home, "end": kVK_End, "page_up": kVK_PageUp, "page_down": kVK_PageDown,
    "f1": kVK_F1, "f2": kVK_F2, "f3": kVK_F3, "f4": kVK_F4, "f5": kVK_F5, "f6": kVK_F6, "f7": kVK_F7, "f8": kVK_F8,
    "f9": kVK_F9, "f10": kVK_F10, "f11": kVK_F11, "f12": kVK_F12,
]

func pressCombo(_ combo: String, repeatCount: Int, expect: String, refuseSecure: Bool) throws {
    var parts = combo.split(separator: "+", omittingEmptySubsequences: true).map(String.init)
    guard let key = parts.popLast(), let code = keyCodes[key] else { throw Refusal("bad_key", "unknown key in \(combo)") }
    for _ in 0..<max(1, repeatCount) {
        try guardInput(expect: expect, refuseSecure: refuseSecure)
        try pressKey(code: CGKeyCode(code), flags: flags(parts))
        pump(0.02)
    }
}

// MARK: - iOS Simulator input

// Touches, keys and hardware buttons go to a booted simulator through SimulatorKit's HID client, the path Simulator.app
// uses itself, so nothing is clicked on this Mac's screen, Simulator.app does not have to be open, and no permission of
// macOS is needed. These are private frameworks of Xcode: everything is looked up at run time, and a missing piece
// makes the operation refuse with "simulator_input_unavailable" instead of crashing. The message builders are the C
// functions SimulatorKit exports; their arguments are what Simulator.app passes (Xcode 26: a point as a fraction of the
// screen with a size of 1×1, no second finger, the digitizer target 0x32 of the main screen).

typealias MouseMessageFn = @convention(c) (UnsafePointer<CGPoint>, UnsafePointer<CGPoint>?, UInt32, UInt, CGSize, UInt) -> UnsafeMutableRawPointer?
typealias KeyMessageFn = @convention(c) (UInt32, UInt32) -> UnsafeMutableRawPointer?
typealias ButtonMessageFn = @convention(c) (UInt32, UInt32, UInt32) -> UnsafeMutableRawPointer?
typealias SendFn = @convention(c) (AnyObject, Selector, UnsafeMutableRawPointer, ObjCBool, DispatchQueue?, (@convention(block) (NSError?) -> Void)?) -> Void

final class SimulatorHID {
    let mouse: MouseMessageFn
    let key: KeyMessageFn
    let button: ButtonMessageFn
    let deviceSet: AnyObject
    var clients: [String: AnyObject] = [:]

    init(developerDir: String) throws {
        let core = "/Library/Developer/PrivateFrameworks/CoreSimulator.framework/CoreSimulator"
        let kit = developerDir + "/Library/PrivateFrameworks/SimulatorKit.framework/SimulatorKit"
        guard dlopen(core, RTLD_NOW) != nil, let handle = dlopen(kit, RTLD_NOW) else {
            throw Refusal("simulator_input_unavailable", "Xcode's simulator frameworks could not be loaded from \(developerDir)")
        }
        guard let m = dlsym(handle, "IndigoHIDMessageForMouseNSEvent"), let k = dlsym(handle, "IndigoHIDMessageForKeyboardArbitrary"),
              let b = dlsym(handle, "IndigoHIDMessageForButton") else {
            throw Refusal("simulator_input_unavailable", "this Xcode's SimulatorKit lacks the HID functions")
        }
        mouse = unsafeBitCast(m, to: MouseMessageFn.self)
        key = unsafeBitCast(k, to: KeyMessageFn.self)
        button = unsafeBitCast(b, to: ButtonMessageFn.self)
        guard let contextClass = NSClassFromString("SimServiceContext") as AnyObject?,
              let context = contextClass.perform(NSSelectorFromString("sharedServiceContextForDeveloperDir:error:"), with: developerDir, with: nil)?.takeUnretainedValue(),
              let set = context.perform(NSSelectorFromString("defaultDeviceSetWithError:"), with: nil)?.takeUnretainedValue() else {
            throw Refusal("simulator_input_unavailable", "CoreSimulator did not open the device set")
        }
        deviceSet = set
    }

    func client(_ udid: String) throws -> AnyObject {
        if let c = clients[udid] { return c }
        guard let uuid = UUID(uuidString: udid) else { throw Refusal("bad_request", "not a device UDID: \(udid)") }
        let devices = deviceSet.perform(NSSelectorFromString("devicesByUDID"))?.takeUnretainedValue() as? NSDictionary
        guard let device = devices?[uuid as NSUUID] as AnyObject? else { throw Refusal("no_device", "no simulator \(udid)") }
        guard let cls = (NSClassFromString("SimulatorKit.SimDeviceLegacyHIDClient") ?? NSClassFromString("SimDeviceLegacyHIDClient")) as AnyObject?,
              let raw = cls.perform(NSSelectorFromString("alloc"))?.takeUnretainedValue(),
              // init consumes what alloc returned and returns its own +1; both are kept on purpose, the client is cached.
              let made = raw.perform(NSSelectorFromString("initWithDevice:error:"), with: device, with: nil)?.takeUnretainedValue() else {
            throw Refusal("simulator_input_unavailable", "the simulator's HID client did not start: is \(udid) booted?")
        }
        clients[udid] = made
        return made
    }

    /// Sends one message and waits for the simulator to take it. The message is freed by SimulatorKit.
    func send(_ udid: String, _ message: UnsafeMutableRawPointer?) throws {
        guard let message = message else { return } // a move closer than a frame to the last one: dropped by design
        let c = try client(udid)
        let sel = NSSelectorFromString("sendWithMessage:freeWhenDone:completionQueue:completion:")
        guard let imp = class_getMethodImplementation(object_getClass(c), sel) else {
            free(message)
            throw Refusal("simulator_input_unavailable", "the HID client cannot send")
        }
        let fn = unsafeBitCast(imp, to: SendFn.self)
        let done = DispatchSemaphore(value: 0)
        var failure: NSError?
        fn(c, sel, message, true, DispatchQueue.global(), { error in
            failure = error
            done.signal()
        })
        if done.wait(timeout: .now() + 5) == .timedOut {
            clients[udid] = nil
            throw Refusal("simulator_input_failed", "the simulator did not take the event in time")
        }
        if let failure = failure {
            clients[udid] = nil
            throw Refusal("simulator_input_failed", failure.localizedDescription)
        }
    }

    /// A touch along points given as fractions of the screen: down at the first, moves through the others, up at the
    /// last. holdMs keeps the finger down before lifting (a long press); stepMs spaces the moves.
    func touch(_ udid: String, points: [CGPoint], holdMs: Int, stepMs: Int) throws {
        guard let first = points.first else { throw Refusal("bad_request", "no points") }
        let unit = CGSize(width: 1, height: 1)
        let target: UInt32 = 0x32
        func message(_ p: CGPoint, _ type: NSEvent.EventType) -> UnsafeMutableRawPointer? {
            var point = CGPoint(x: min(max(p.x, 0), 1), y: min(max(p.y, 0), 1))
            return mouse(&point, nil, target, type.rawValue, unit, 0)
        }
        try send(udid, message(first, .leftMouseDown))
        var last = first
        for p in points.dropFirst() {
            // SimulatorKit drops a move that comes within 16 ms of the one before.
            Thread.sleep(forTimeInterval: Double(max(stepMs, 20)) / 1000)
            try send(udid, message(p, .leftMouseDragged))
            last = p
        }
        if holdMs > 0 { Thread.sleep(forTimeInterval: Double(min(holdMs, 10000)) / 1000) }
        try send(udid, message(last, .leftMouseUp))
    }

    /// Presses HID keyboard usages: each entry is [usage, modifier usages...], modifiers held around the key.
    func keys(_ udid: String, _ presses: [[Int]]) throws {
        for press in presses {
            guard let usage = press.first else { continue }
            let mods = press.dropFirst().map { UInt32($0) }
            for m in mods { try send(udid, key(m, 1)) }
            try send(udid, key(UInt32(usage), 1))
            try send(udid, key(UInt32(usage), 2))
            for m in mods.reversed() { try send(udid, key(m, 2)) }
            Thread.sleep(forTimeInterval: 0.012)
        }
    }

    /// A hardware button: home, lock (the side button of newer devices), siri, apple_pay.
    func press(_ udid: String, button name: String, holdMs: Int) throws {
        let sources: [String: UInt32] = ["home": 0x0, "lock": 0x1, "side_button": 0xbb8, "siri": 0x400002, "apple_pay": 0x1f4]
        guard let source = sources[name] else { throw Refusal("bad_request", "unknown button \(name)") }
        let hardware: UInt32 = 0x33
        try send(udid, button(source, 1, hardware))
        if holdMs > 0 { Thread.sleep(forTimeInterval: Double(min(holdMs, 10000)) / 1000) }
        try send(udid, button(source, 2, hardware))
    }
}

var simulatorHID: SimulatorHID?

func simulator(_ developerDir: String) throws -> SimulatorHID {
    if let s = simulatorHID { return s }
    let dir = developerDir.isEmpty ? "/Applications/Xcode.app/Contents/Developer" : developerDir
    let s = try SimulatorHID(developerDir: dir)
    simulatorHID = s
    return s
}

// MARK: - Requests

func handle(_ req: [String: Any]) throws -> Any? {
    func text(_ key: String) -> String { req[key] as? String ?? "" }
    func number(_ key: String) -> Double { (req[key] as? NSNumber)?.doubleValue ?? 0 }
    func int(_ key: String) -> Int { (req[key] as? NSNumber)?.intValue ?? 0 }
    func flag(_ key: String) -> Bool { req[key] as? Bool ?? false }
    func point(_ x: String = "x", _ y: String = "y") -> CGPoint { CGPoint(x: number(x), y: number(y)) }
    switch text("op") {
    case "ping": return ["pong": true]
    case "permissions": return ["screen_recording": CGPreflightScreenCaptureAccess(), "accessibility": AXIsProcessTrusted()]
    case "apps": return listApps(installed: flag("installed"))
    case "frontmost": pump(0); return appObject(NSWorkspace.shared.frontmostApplication)
    case "app_at": return appAt(point())
    case "windows": return windows(of: text("app"))
    case "focused": return focused()
    case "activate": try activate(text("app")); return nil
    case "focus_window": try focusWindow(appID: text("app"), windowID: int("window")); return nil
    case "click":
        try click(at: point(), right: text("button") == "right", count: int("count"), modifiers: req["modifiers"] as? [String] ?? [], expect: text("expect"))
        return nil
    case "drag": try drag(from: point(), to: point("x2", "y2"), expect: text("expect")); return nil
    case "release_mouse":
        guard AXIsProcessTrusted() else { throw Refusal("no_permission", "Accessibility is not allowed") }
        try releaseMouse()
        return nil
    case "scroll": try scroll(at: point(), dx: int("dx"), dy: int("dy"), expect: text("expect")); return nil
    case "type": try typeText(text("text"), expect: text("expect"), refuseSecure: flag("refuse_secure")); return nil
    case "key": try pressCombo(text("combo"), repeatCount: int("repeat"), expect: text("expect"), refuseSecure: flag("refuse_secure")); return nil
    case "find":
        guard AXIsProcessTrusted() else { throw Refusal("no_permission", "Accessibility is not allowed") }
        return try find(appID: text("app"), windowID: int("window"), query: text("query"), role: text("role"), max: int("max"))
    case "menu": try pressMenu(appID: text("app"), path: req["path"] as? [String] ?? []); return nil
    case "secure_fields":
        guard AXIsProcessTrusted() else { throw Refusal("no_permission", "Accessibility is not allowed") }
        return try secureFields(appID: text("app"), windowID: int("window"))
    case "sim_probe": _ = try simulator(text("developer_dir")); return ["ok": true]
    case "sim_touch":
        let raw = req["points"] as? [[NSNumber]] ?? []
        let points = raw.compactMap { $0.count == 2 ? CGPoint(x: $0[0].doubleValue, y: $0[1].doubleValue) : nil }
        try simulator(text("developer_dir")).touch(text("udid"), points: points, holdMs: int("hold_ms"), stepMs: int("step_ms"))
        return nil
    case "sim_keys":
        let presses = (req["presses"] as? [[NSNumber]] ?? []).map { $0.map(\.intValue) }
        try simulator(text("developer_dir")).keys(text("udid"), presses)
        return nil
    case "sim_button":
        try simulator(text("developer_dir")).press(text("udid"), button: text("button"), holdMs: int("hold_ms"))
        return nil
    default: throw Refusal("bad_op", "unknown operation \(text("op"))")
    }
}

func serve() -> Never {
    DispatchQueue.global().async {
        while let line = readLine(strippingNewline: true) {
            // On the main thread, whose run loop keeps NSWorkspace up to date.
            DispatchQueue.main.async {
                guard let data = line.data(using: .utf8),
                      let req = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
                    reply(["ok": false, "code": "bad_request", "error": "not a JSON object"])
                    return
                }
                do {
                    var out: [String: Any] = ["ok": true]
                    if let result = try handle(req) { out["result"] = result }
                    reply(out)
                } catch let refusal as Refusal {
                    reply(["ok": false, "code": refusal.code, "error": refusal.message])
                } catch {
                    reply(["ok": false, "code": "error", "error": "\(error)"])
                }
            }
        }
        // After what is already queued.
        DispatchQueue.main.async { exit(0) }
    }
    RunLoop.main.run()
    exit(0)
}

// MARK: - Indicator

final class StopTarget: NSObject {
    @objc func stop() {
        FileHandle.standardOutput.write(Data("stop\n".utf8))
    }
}

func runIndicator(label: String) -> Never {
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    let size = NSSize(width: 340, height: 34)
    let panel = NSPanel(contentRect: NSRect(origin: .zero, size: size), styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
    panel.level = .statusBar
    panel.collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
    panel.isFloatingPanel = true
    panel.hidesOnDeactivate = false
    panel.sharingType = .none
    panel.backgroundColor = NSColor(calibratedRed: 0.72, green: 0.16, blue: 0.14, alpha: 0.96)
    let title = NSTextField(labelWithString: label)
    title.textColor = .white
    title.font = .systemFont(ofSize: 12, weight: .medium)
    title.lineBreakMode = .byTruncatingTail
    title.frame = NSRect(x: 12, y: 8, width: size.width - 84, height: 18)
    let target = StopTarget()
    let button = NSButton(title: "Stop", target: target, action: #selector(StopTarget.stop))
    button.bezelStyle = .rounded
    button.frame = NSRect(x: size.width - 66, y: 3, width: 60, height: 28)
    panel.contentView?.addSubview(title)
    panel.contentView?.addSubview(button)
    if let screen = NSScreen.main?.visibleFrame {
        panel.setFrameOrigin(NSPoint(x: screen.midX - size.width / 2, y: screen.maxY - size.height - 8))
    }
    panel.orderFrontRegardless()
    DispatchQueue.global().async {
        while let line = readLine(strippingNewline: true) {
            let text = (try? JSONSerialization.jsonObject(with: Data(line.utf8), options: .fragmentsAllowed)) as? String ?? line
            DispatchQueue.main.async { title.stringValue = text }
        }
        exit(0)
    }
    withExtendedLifetime(target) { app.run() }
    exit(0)
}

// MARK: - Recording a demo

// With --record the helper watches what the user does, for a demo the agent turns into a skill: clicks, scrolls, key
// presses and app switches, one JSON object per line on stdout. It only listens (a listen-only event tap: nothing is
// changed or delayed), shows a red panel with a Stop button while it runs, and stops on ⌃⌥⌘. as well. Keystrokes are
// dropped, not just hidden, while secure input is on or a secure field has the focus: only {"type":"secure_key"} says
// one happened. Everything else (which apps count, the screenshots, the blanking) is the client's.

func emit(_ object: [String: Any]) {
    var o = object
    o["t"] = Date().timeIntervalSince1970
    reply(o)
}

func elementAt(_ point: CGPoint) -> [String: Any] {
    var out: [String: Any] = ["role": "", "title": "", "description": "", "secure": false]
    guard AXIsProcessTrusted() else { return out }
    let system = AXUIElementCreateSystemWide()
    AXUIElementSetMessagingTimeout(system, 0.5)
    var raw: AXUIElement?
    guard AXUIElementCopyElementAtPosition(system, Float(point.x), Float(point.y), &raw) == .success, let element = raw else { return out }
    out["role"] = string(element, kAXRoleAttribute)
    out["title"] = String(string(element, kAXTitleAttribute).prefix(200))
    out["description"] = String(string(element, kAXDescriptionAttribute).prefix(200))
    out["secure"] = isSecure(element)
    if (out["title"] as? String ?? "").isEmpty && !isSecure(element) {
        // A button's text is often on its child; a field's label on the element it names.
        if let first = children(element).first { out["title"] = String(string(first, kAXTitleAttribute).prefix(200)) }
    }
    if let box = frame(of: element) { out["frame"] = ["x": box.origin.x, "y": box.origin.y, "w": box.width, "h": box.height] }
    return out
}

func windowAt(_ point: CGPoint, pid: Int32) -> [String: Any]? {
    for info in onScreenWindows() {
        guard let owner = info[kCGWindowOwnerPID as String] as? Int32, owner == pid,
              (info[kCGWindowLayer as String] as? Int ?? 0) == 0,
              let number = info[kCGWindowNumber as String] as? Int,
              let box = bounds(of: info), box.contains(point) else { continue }
        return ["id": number, "title": info[kCGWindowName as String] as? String ?? "", "x": box.origin.x, "y": box.origin.y, "w": box.width, "h": box.height]
    }
    return nil
}

func frontWindow(pid: Int32) -> [String: Any]? {
    for info in onScreenWindows() {
        guard let owner = info[kCGWindowOwnerPID as String] as? Int32, owner == pid,
              (info[kCGWindowLayer as String] as? Int ?? 0) == 0,
              let number = info[kCGWindowNumber as String] as? Int, let box = bounds(of: info), box.width > 1 else { continue }
        return ["id": number, "title": info[kCGWindowName as String] as? String ?? "", "x": box.origin.x, "y": box.origin.y, "w": box.width, "h": box.height]
    }
    return nil
}

/// Whether typed keys must be dropped: secure input is on, or the focused element is a secure field.
func typingIsSecret() -> Bool {
    if IsSecureEventInputEnabled() { return true }
    return focused()["secure"] as? Bool ?? false
}

let keyNames: [Int: String] = Dictionary(uniqueKeysWithValues: keyCodes.map { ($0.value, $0.key) })

func modifierNames(_ flags: CGEventFlags) -> [String] {
    var out: [String] = []
    if flags.contains(.maskControl) { out.append("ctrl") }
    if flags.contains(.maskAlternate) { out.append("alt") }
    if flags.contains(.maskShift) { out.append("shift") }
    if flags.contains(.maskCommand) { out.append("cmd") }
    return out
}

var recorderTap: CFMachPort?
let ownPID = ProcessInfo.processInfo.processIdentifier

func recorded(_ type: CGEventType, _ event: CGEvent) {
    let point = event.location
    switch type {
    case .leftMouseDown, .rightMouseDown:
        let app = NSRunningApplication(processIdentifier: Int32(appAt(point)["pid"] as? Int ?? 0))
        let pid = app?.processIdentifier ?? 0
        if pid == ownPID { return } // the panel's own Stop button
        var o: [String: Any] = [
            "type": "click", "x": point.x, "y": point.y, "button": type == .rightMouseDown ? "right" : "left",
            "count": Int(event.getIntegerValueField(.mouseEventClickState)), "modifiers": modifierNames(event.flags),
            "app": appObject(app), "element": elementAt(point),
        ]
        if let w = windowAt(point, pid: pid) { o["window"] = w }
        emit(o)
    case .scrollWheel:
        let app = NSRunningApplication(processIdentifier: Int32(appAt(point)["pid"] as? Int ?? 0))
        if app?.processIdentifier == ownPID { return }
        var o: [String: Any] = [
            "type": "scroll", "x": point.x, "y": point.y,
            "dy": -Int(event.getIntegerValueField(.scrollWheelEventDeltaAxis1)), "dx": -Int(event.getIntegerValueField(.scrollWheelEventDeltaAxis2)),
            "app": appObject(app),
        ]
        if let app = app, let w = windowAt(point, pid: app.processIdentifier) { o["window"] = w }
        emit(o)
    case .keyDown:
        let front = NSWorkspace.shared.frontmostApplication
        var o: [String: Any] = ["app": appObject(front)]
        if let front = front, let w = frontWindow(pid: front.processIdentifier) { o["window"] = w }
        if typingIsSecret() {
            o["type"] = "secure_key"
            emit(o)
            return
        }
        let code = Int(event.getIntegerValueField(.keyboardEventKeycode))
        let mods = modifierNames(event.flags)
        var length = 0
        var chars = [UniChar](repeating: 0, count: 8)
        event.keyboardGetUnicodeString(maxStringLength: 8, actualStringLength: &length, unicodeString: &chars)
        let text = String(utf16CodeUnits: chars, count: length)
        o["type"] = "key"
        o["key"] = keyNames[code] ?? ""
        o["modifiers"] = mods
        // Only plain characters count as typing; a combination is a command.
        let commandLike = mods.contains("cmd") || mods.contains("ctrl")
        if !commandLike, let scalar = text.unicodeScalars.first, !CharacterSet.controlCharacters.contains(scalar) { o["text"] = text }
        emit(o)
    default:
        break
    }
}

func isStopHotkey(_ event: CGEvent) -> Bool {
    let f = event.flags
    return Int(event.getIntegerValueField(.keyboardEventKeycode)) == kVK_ANSI_Period
        && f.contains(.maskControl) && f.contains(.maskAlternate) && f.contains(.maskCommand)
}

final class RecordStop: NSObject {
    @objc func stop() { emit(["type": "stop", "reason": "button"]) }
}

func runRecorder(label: String) -> Never {
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    guard AXIsProcessTrusted() else {
        emit(["type": "error", "error": "Accessibility is not allowed"])
        exit(1)
    }
    let mask: CGEventMask = (1 << CGEventType.leftMouseDown.rawValue) | (1 << CGEventType.rightMouseDown.rawValue)
        | (1 << CGEventType.keyDown.rawValue) | (1 << CGEventType.scrollWheel.rawValue)
    let callback: CGEventTapCallBack = { _, type, event, _ in
        if type == .tapDisabledByTimeout || type == .tapDisabledByUserInput {
            if let tap = recorderTap { CGEvent.tapEnable(tap: tap, enable: true) }
            return Unmanaged.passUnretained(event)
        }
        if type == .keyDown && isStopHotkey(event) {
            emit(["type": "stop", "reason": "hotkey"])
            return Unmanaged.passUnretained(event)
        }
        // Looked at after the event went on, so the tap never holds anything up.
        let copy = event.copy() ?? event
        DispatchQueue.main.async { recorded(type, copy) }
        return Unmanaged.passUnretained(event)
    }
    guard let tap = CGEvent.tapCreate(tap: .cgSessionEventTap, place: .tailAppendEventTap, options: .listenOnly, eventsOfInterest: mask, callback: callback, userInfo: nil) else {
        emit(["type": "error", "error": "macOS refused to let the helper watch input. Allow Accessibility (or Input Monitoring) for the Harness app"])
        exit(1)
    }
    recorderTap = tap
    let source = CFMachPortCreateRunLoopSource(nil, tap, 0)
    CFRunLoopAddSource(CFRunLoopGetMain(), source, .commonModes)
    CGEvent.tapEnable(tap: tap, enable: true)
    NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didActivateApplicationNotification, object: nil, queue: .main) { note in
        let a = note.userInfo?[NSWorkspace.applicationUserInfoKey] as? NSRunningApplication
        if a?.processIdentifier == ownPID { return }
        var o: [String: Any] = ["type": "activate", "app": appObject(a)]
        if let a = a, let w = frontWindow(pid: a.processIdentifier) { o["window"] = w }
        emit(o)
    }

    let size = NSSize(width: 380, height: 34)
    let panel = NSPanel(contentRect: NSRect(origin: .zero, size: size), styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
    panel.level = .statusBar
    panel.collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
    panel.isFloatingPanel = true
    panel.hidesOnDeactivate = false
    panel.sharingType = .none
    panel.backgroundColor = NSColor(calibratedRed: 0.72, green: 0.16, blue: 0.14, alpha: 0.96)
    let title = NSTextField(labelWithString: label)
    title.textColor = .white
    title.font = .systemFont(ofSize: 12, weight: .medium)
    title.lineBreakMode = .byTruncatingTail
    title.frame = NSRect(x: 12, y: 8, width: size.width - 84, height: 18)
    let target = RecordStop()
    let button = NSButton(title: "Stop", target: target, action: #selector(RecordStop.stop))
    button.bezelStyle = .rounded
    button.frame = NSRect(x: size.width - 66, y: 3, width: 60, height: 28)
    panel.contentView?.addSubview(title)
    panel.contentView?.addSubview(button)
    if let screen = NSScreen.main?.visibleFrame {
        panel.setFrameOrigin(NSPoint(x: screen.midX - size.width / 2, y: screen.maxY - size.height - 8))
    }
    panel.orderFrontRegardless()
    if let front = NSWorkspace.shared.frontmostApplication, front.processIdentifier != ownPID {
        var o: [String: Any] = ["type": "activate", "app": appObject(front)]
        if let w = frontWindow(pid: front.processIdentifier) { o["window"] = w }
        emit(o)
    }
    emit(["type": "started"])
    DispatchQueue.global().async {
        while let line = readLine(strippingNewline: true) {
            let text = (try? JSONSerialization.jsonObject(with: Data(line.utf8), options: .fragmentsAllowed)) as? String ?? line
            DispatchQueue.main.async { title.stringValue = text }
        }
        DispatchQueue.main.async { exit(0) }
    }
    withExtendedLifetime((target, source)) { app.run() }
    exit(0)
}

// MARK: - Main

let arguments = CommandLine.arguments
if arguments.count >= 2 && arguments[1] == "--indicator" {
    runIndicator(label: arguments.count >= 3 ? arguments[2] : "Harness is controlling this machine")
}
if arguments.count >= 2 && arguments[1] == "--record" {
    runRecorder(label: arguments.count >= 3 ? arguments[2] : "Recording a demo. ⌃⌥⌘. or Stop ends it")
}
serve()
