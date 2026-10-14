//go:build unix

package main

import (
	"fmt"

	"github.com/ebitengine/purego"
)

// platformLoadLibrary 用 dlopen 加载 libpdfium.so (Unix-only, purego 的 Dlopen 在非 Unix 平台不存在)
func platformLoadLibrary() (uintptr, error) {
	for _, name := range []string{"./libpdfium.so", "libpdfium.so"} {
		lib, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil && lib != 0 {
			return lib, nil
		}
	}
	return 0, fmt.Errorf("无法加载 PDFium 动态库，请将 libpdfium.so 放在本程序同目录")
}
