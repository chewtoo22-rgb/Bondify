//go:build windows

package main

import (
	"context"
	"log"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file is Bondify's Windows system tray icon: double-click opens the diagnostics page
// in the default browser, right-click gives a one-item "Quit" menu that gracefully shuts
// the tunnel down via cancel. It is raw Win32 (a hidden window + Shell_NotifyIconW), not a
// third-party tray library -- consistent with the rest of the codebase's stdlib-only,
// no-CGO approach (see core/tun/windows.go). None of it has been exercised on a real
// Windows machine or session (this repo is developed and CI-tested on Linux runners that
// can cross-compile Windows binaries but cannot run them) -- see ARCHITECTURE.md §9 and
// README's "Verified so far (Phase 6)" section for exactly what was and wasn't verified.
//
// user32.dll/shell32.dll have no golang.org/x/sys/windows bindings (that package covers
// syscalls, sockets, and services, not GUI/shell APIs), so the handful of functions and
// structs needed are declared here directly against their documented Win32 ABI layouts.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procSetForegroundWin = user32.NewProc("SetForegroundWindow")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmDestroy       = 0x0002
	wmCommand       = 0x0111
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmAppTrayMsg    = 0x8000 + 1 // WM_APP + 1, this window's Shell_NotifyIcon callback message

	nimAdd    = 0x00000000
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString       = 0x00000000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	idiApplication = 32512 // built-in default app icon, by resource ordinal

	menuIDQuit = 1001
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type point struct{ x, y int32 }

type msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

// notifyIconDataW mirrors the Vista+ (V3) NOTIFYICONDATAW layout. Only the fields this file
// actually sets (hWnd/uID/uFlags/uCallbackMessage/hIcon/szTip) are meaningful; the rest
// exist purely so cbSize (which Shell_NotifyIconW uses to detect which struct version it
// was handed) matches the real, documented size of the struct at this API level.
type notifyIconDataW struct {
	cbSize            uint32
	hWnd              windows.Handle
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             windows.Handle
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uVersionOrTimeout uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          windows.GUID
	hBalloonIcon      windows.Handle
}

var (
	trayCancel   context.CancelFunc
	trayDiagURL  string
	trayHWND     windows.Handle
	trayIconData notifyIconDataW
)

// startTray runs Bondify's Windows tray icon; it blocks in a Win32 message loop until Quit
// is chosen from the icon's context menu (which calls cancel) or the window is otherwise
// destroyed, so callers should run it in its own goroutine. diagURL is opened in the
// default browser on double-click, empty disables that (diagnostics endpoint was disabled).
func startTray(cancel context.CancelFunc, diagURL string) {
	trayCancel = cancel
	trayDiagURL = diagURL

	className, err := windows.UTF16PtrFromString("BondifyTrayWndClass")
	if err != nil {
		log.Printf("client: tray: class name: %v", err)
		return
	}
	hInstance, _, _ := procGetModuleHandleW.Call(0)

	wc := wndClassExW{
		lpfnWndProc:   windows.NewCallback(trayWndProc),
		hInstance:     windows.Handle(hInstance),
		lpszClassName: className,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if ret, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		log.Printf("client: tray: RegisterClassExW: %v", err)
		return
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0, 0, 0, hInstance, 0)
	if hwnd == 0 {
		log.Printf("client: tray: CreateWindowExW: %v", err)
		return
	}
	trayHWND = windows.Handle(hwnd)

	hIcon, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))

	trayIconData = notifyIconDataW{
		hWnd:             trayHWND,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmAppTrayMsg,
		hIcon:            windows.Handle(hIcon),
	}
	trayIconData.cbSize = uint32(unsafe.Sizeof(trayIconData))
	copy(trayIconData.szTip[:], windows.StringToUTF16("Bondify"))
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&trayIconData)))
	defer procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&trayIconData)))

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if ret == 0 || int32(ret) == -1 {
			return // WM_QUIT, or GetMessage failed
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func trayWndProc(hwnd windows.Handle, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmAppTrayMsg:
		switch lParam {
		case wmLButtonDblClk:
			if trayDiagURL != "" {
				openBrowser(trayDiagURL)
			}
		case wmRButtonUp:
			showTrayMenu(hwnd)
		}
		return 0
	case wmCommand:
		if loWord(uint32(wParam)) == menuIDQuit {
			if trayCancel != nil {
				trayCancel()
			}
			procPostQuitMessage.Call(0)
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return ret
}

func showTrayMenu(hwnd windows.Handle) {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)
	quitLabel, _ := windows.UTF16PtrFromString("Quit")
	procAppendMenuW.Call(hMenu, mfString, menuIDQuit, uintptr(unsafe.Pointer(quitLabel)))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Required for the popup menu to close itself when it loses focus (documented
	// Shell_NotifyIcon / TrackPopupMenu idiom for tray-icon context menus).
	procSetForegroundWin.Call(uintptr(hwnd))
	procTrackPopupMenu.Call(hMenu, tpmRightButton|tpmReturnCmd, uintptr(pt.x), uintptr(pt.y), 0, uintptr(hwnd), 0)
}

func openBrowser(url string) {
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start(); err != nil {
		log.Printf("client: tray: open browser: %v", err)
	}
}

func loWord(v uint32) uint32 { return v & 0xffff }
