//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// platformLoadLibrary 用 syscall 调 kernel32!LoadLibraryA (Windows-only)
func platformLoadLibrary() (uintptr, error) {
	kernel32, err := syscall.LoadLibrary("kernel32.dll")
	if err != nil {
		return 0, err
	}
	defer syscall.FreeLibrary(kernel32)
	proc, err := syscall.GetProcAddress(kernel32, "LoadLibraryA")
	if err != nil {
		return 0, err
	}
	nameBytes, _ := syscall.BytePtrFromString("pdfium.dll")
	h, _, e := syscall.Syscall(proc, 1, uintptr(unsafe.Pointer(nameBytes)), 0, 0)
	if h == 0 {
		return 0, fmt.Errorf("无法加载 PDFium 动态库 (%v)，请将 pdfium.dll 放在本程序同目录", e)
	}
	return h, nil
}
