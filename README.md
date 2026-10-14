# pdf2word

把目录里的 PDF 批量转成**图片型 Word（.docx）**——每一页渲染成一张图片嵌入，不做 OCR、不编辑内容、逐页原样保留。

单文件可执行程序，无需安装任何运行时。支持 Windows 7 及以上、Linux。

[English](#english) · [中文](#中文)

---

## 中文

### 特性

- **图片型转换**：每一页 PDF 渲染为 JPEG 嵌入 docx，不做 OCR、不改内容
- **批量**：扫描整个目录（可选递归子目录），每个 PDF 生成同名 docx
- **单文件**：Go 编译出独立 exe，双击即用，无需 .NET / Java / Python
- **体积小**：主程序约 2MB，配 PDFium 引擎（约 5MB）
- **跨平台**：Windows 7+ (x64)、Linux (x64)
- **快**：113 页扫描件约 12 秒转完

### 下载

到 [Releases](../../releases) 下载对应系统的压缩包：

| 文件 | 适用系统 |
|---|---|
| `pdf2word_win7_x64.zip` | Windows 7 / 8 / 10 / 11 64位（兼容模式编译） |
| `pdf2word_win_x64.zip` | Windows 10 / 11 64位（性能更好） |
| `pdf2word_linux_x64.tar.gz` | Linux 64位 |

每个压缩包含 `pdf2word` 主程序 + `pdfium` 动态库 + 说明文档。**两者必须放同一目录。**

### 用法

```
pdf2word.exe                转换当前目录所有 PDF
pdf2word.exe 文件夹路径     转换指定目录（可把文件夹拖到 exe 图标上）
pdf2word.exe -r 文件夹      递归子目录
pdf2word.exe -o 输出目录 文件夹   指定输出位置
pdf2word.exe -dpi 200 文件夹     渲染清晰度（默认 150）
pdf2word.exe -q 90 文件夹        JPEG 质量 1-100（默认 85）
pdf2word.exe -f 文件夹           覆盖已存在的 docx
```

也可以直接把 PDF 文件夹**拖到 exe 图标上**。

### 输出

每个 `xx.pdf` 在同目录生成 `xx.docx`。已存在的 docx 默认跳过（不覆盖，加 `-f` 强制覆盖）。

### 从源码构建

依赖：Go 1.20+，以及对应平台的 [PDFium 预编译库](https://github.com/bblanchon/pdfium-binaries)。

```bash
# 克隆
git clone https://github.com/huanxiang666/pdf2word.git
cd pdf2word

# 放入 PDFium 库（与可执行文件同目录）：
#   Windows: pdfium.dll    Linux: libpdfium.so

# 编译
go build -ldflags="-s -w" -o pdf2word .
```

**Windows 7 兼容编译**：用 Go 1.20（最后支持 Win7 的 Go 版本），并把 pdfium.dll 换成老版本（如 chromium/4929，静态链接、无 VC 运行时依赖）。新版 Go 编出的程序目标子系统版本为 Win10，Win7 无法加载。

**交叉编译 Windows 版**（在 Linux 上）：

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o pdf2word.exe .
```

纯 Go 实现（PDFium 通过 [purego](https://github.com/ebitengine/purego) 动态加载，零 cgo），所以无需 Windows 编译环境即可交叉编译。

### 技术说明

- **PDF 渲染**：Google [PDFium](https://pdfium.googlesource.com/pdfium/)，经 purego 动态绑定（不走 cgo，交叉编译友好）
- **docx 生成**：标准库 `archive/zip` 手写最小 OOXML，每张图片一页
- **内存安全**：PDFium 持有文件内存期间用 `runtime.KeepAlive` 防止 GC 提前回收

### 协议

[MIT](LICENSE)。PDFium 引擎本身遵循 [Apache-2.0](https://pdfium.googlesource.com/pdfium/+/refs/heads/main/LICENSE)。

---

## English

Batch-convert PDFs in a directory into **image-based Word (.docx)** files — every page is rendered to an image and embedded as-is. No OCR, no content editing, exact page-by-page fidelity.

Single self-contained binary, no runtime required. Windows 7+ and Linux.

### Features

- **Image-based conversion**: each PDF page rendered to JPEG and embedded; no OCR, no edits
- **Batch**: scan a whole directory (optionally recursive), one docx per PDF
- **Single binary**: standalone exe via Go, double-click to run
- **Small**: ~2MB binary + ~5MB PDFium engine
- **Cross-platform**: Windows 7+ (x64), Linux (x64)
- **Fast**: a 113-page scanned PDF converts in ~12s

### Usage

```
pdf2word.exe                convert all PDFs in current directory
pdf2word.exe <folder>       convert a specific folder (drag-and-drop supported)
pdf2word.exe -r <folder>    recurse into subdirectories
pdf2word.exe -o <outdir> <folder>   custom output directory
pdf2word.exe -dpi 200 <folder>      render DPI (default 150)
pdf2word.exe -q 90 <folder>         JPEG quality 1-100 (default 85)
pdf2word.exe -f <folder>            overwrite existing docx
```

### Build from source

Requires Go 1.20+ and the [PDFium prebuilt library](https://github.com/bblanchon/pdfium-binaries) for your platform, placed next to the binary (`pdfium.dll` on Windows, `libpdfium.so` on Linux).

```bash
git clone https://github.com/huanxiang666/pdf2word.git
cd pdf2word
go build -ldflags="-s -w" -o pdf2word .
```

For **Windows 7 compatibility**, build with Go 1.20 and use an older statically-linked pdfium.dll (e.g. chromium/4929).

### License

[MIT](LICENSE). The PDFium engine is [Apache-2.0](https://pdfium.googlesource.com/pdfium/+/refs/heads/main/LICENSE).
