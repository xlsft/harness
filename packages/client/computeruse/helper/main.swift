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

// MARK: - Main

let arguments = CommandLine.arguments
if arguments.count >= 2 && arguments[1] == "--indicator" {
    runIndicator(label: arguments.count >= 3 ? arguments[2] : "Harness is controlling this machine")
}
serve()
