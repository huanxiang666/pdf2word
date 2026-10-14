// pdf2word — PDF 转图片型 Word 小程序
// 用法:
//   pdf2word.exe               转换当前目录所有 PDF
//   pdf2word.exe <目录>        转换指定目录（支持把文件夹拖到 exe 上）
//   pdf2word.exe -r <目录>     递归子目录
// 每个 PDF 转出同名 .docx，每一页是一张 JPEG 图片，不做 OCR、不编辑内容。
package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ======================= PDFium 绑定 (purego, 无 cgo) =======================

var (
	pdfLib          uintptr
	fnInitLibrary   func()
	fnLoadMemDoc    func(data unsafe.Pointer, size int32, pwd unsafe.Pointer) unsafe.Pointer
	fnGetPageCount  func(doc unsafe.Pointer) int32
	fnLoadPage      func(doc unsafe.Pointer, idx int32) unsafe.Pointer
	fnGetPageWidth  func(page unsafe.Pointer) float64
	fnGetPageHeight func(page unsafe.Pointer) float64
	fnClosePage     func(page unsafe.Pointer)
	fnCloseDoc      func(doc unsafe.Pointer)
	fnDestroyLib    func()
	fnBmpCreateEx   func(w, h, format int32, first unsafe.Pointer, stride int32) unsafe.Pointer
	fnBmpDestroy    func(bmp unsafe.Pointer)
	fnRenderBmp     func(bmp, page unsafe.Pointer, sx, sy, sw, sh, rotate, flags int32)
	fnBmpGetBuffer  func(bmp unsafe.Pointer) unsafe.Pointer
)

// PDFium 常量
const (
	fpdfBitmapBGRA = 4     // FPDFBitmap_BGRA
	fpdfAnnot      = 0x01  // FPDF_ANNOT
	fpdfLcdText    = 0x02  // FPDF_LCD_TEXT
)

var pdfiumMu sync.Mutex // PDFium 非线程安全，全程串行

// RGBA 像素缓冲池: 避免每页新分配 ~8.7MB, 降低内存足迹
var rgbaPool = sync.Pool{New: func() any { b := make([]byte, 0, 4*1241*1755); return &b }}

func loadPdfium() error {
	// 平台加载逻辑在 load_unix.go / load_windows.go 中实现
	lib, err := platformLoadLibrary()
	if err != nil {
		return err
	}
	pdfLib = lib
	reg := func(dst interface{}, name string) {
		purego.RegisterLibFunc(dst, pdfLib, name)
	}
	reg(&fnInitLibrary, "FPDF_InitLibrary")
	reg(&fnLoadMemDoc, "FPDF_LoadMemDocument")
	reg(&fnGetPageCount, "FPDF_GetPageCount")
	reg(&fnLoadPage, "FPDF_LoadPage")
	reg(&fnGetPageWidth, "FPDF_GetPageWidth")
	reg(&fnGetPageHeight, "FPDF_GetPageHeight")
	reg(&fnClosePage, "FPDF_ClosePage")
	reg(&fnCloseDoc, "FPDF_CloseDocument")
	reg(&fnDestroyLib, "FPDF_DestroyLibrary")
	reg(&fnBmpCreateEx, "FPDFBitmap_CreateEx")
	reg(&fnBmpDestroy, "FPDFBitmap_Destroy")
	reg(&fnRenderBmp, "FPDF_RenderPageBitmap")
	reg(&fnBmpGetBuffer, "FPDFBitmap_GetBuffer")
	// 新版 PDFium 强制要求: 任何处理函数调用前必须先初始化
	fnInitLibrary()
	return nil
}

// renderPage 从已打开的文档渲染单页为 JPEG 字节流 (文档需预先加载), 返回图片与页面原始尺寸(pt)
func renderPage(doc unsafe.Pointer, pageIdx int, dpi, quality int) ([]byte, float64, float64, error) {
	// pdfiumMu.Lock() // 实验: 暂时关闭
	// defer pdfiumMu.Unlock()

	// 加载页(带重试, 抵御系统内存紧张时 C 分配的间歇性失败)
	var page unsafe.Pointer
	for try := 0; try < 3; try++ {
		page = fnLoadPage(doc, int32(pageIdx))
		if page != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if page == nil {
		return nil, 0, 0, fmt.Errorf("无法加载第 %d 页", pageIdx+1)
	}
	defer fnClosePage(page)

	ptW := fnGetPageWidth(page)
	ptH := fnGetPageHeight(page)
	if ptW <= 0 || ptH <= 0 {
		ptW, ptH = 595, 842 // 兜底 A4
	}
	scale := float64(dpi) / 72.0
	pxW := int(ptW*scale + 0.5)
	pxH := int(ptH*scale + 0.5)

	bmp := fnBmpCreateEx(int32(pxW), int32(pxH), fpdfBitmapBGRA, nil, 0)
	if bmp == nil {
		return nil, 0, 0, fmt.Errorf("位图创建失败")
	}
	defer fnBmpDestroy(bmp)

	// 全页渲染: start=(0,0), size=(pxW,pxH), 不旋转, 注释+LCD文本
	fnRenderBmp(bmp, page, 0, 0, int32(pxW), int32(pxH), 0, fpdfAnnot|fpdfLcdText)

	buf := fnBmpGetBuffer(bmp)
	if buf == nil {
		return nil, 0, 0, fmt.Errorf("取位图缓冲区失败")
	}
	raw := unsafe.Slice((*byte)(buf), pxW*pxH*4)

	// BGRA -> RGBA (swizzle)
	rgba := image.NewRGBA(image.Rect(0, 0, pxW, pxH))
	dst := rgba.Pix
	for i := 0; i < len(raw); i += 4 {
		dst[i] = raw[i+2]   // R
		dst[i+1] = raw[i+1] // G
		dst[i+2] = raw[i]   // B
		dst[i+3] = 0xFF
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, rgba, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, err
	}
	return out.Bytes(), ptW, ptH, nil
}

// ======================= 极简 OOXML docx 写入器 =======================

// twips/EMU 常量
const (
	emuPerTwip = 635
	a4WTwips   = 11906 // A4 纵向宽
	a4HTwips   = 16838 // A4 纵向高
)

type pageItem struct {
	relID string // word/_rels/document.xml.rels 中的 rId
	wEMU  int    // 图片宽 EMU
	hEMU  int    // 图片高 EMU
}

type docxBuilder struct {
	zip      *zip.Writer
	mediaN   int
	pages    []pageItem
	portrait bool // 页面方向
}

func newDocxBuilder(w io.Writer, portrait bool) (*docxBuilder, error) {
	zw := zip.NewWriter(w)
	return &docxBuilder{zip: zw, portrait: portrait}, nil
}

// addPage 写入一页 JPEG 图片, 返回该页在 docx 中的元数据
func (b *docxBuilder) addPage(jpegData []byte, pageWpt, pageHpt float64) error {
	b.mediaN++
	name := fmt.Sprintf("word/media/image%d.jpeg", b.mediaN)
	fw, err := b.zip.Create(name)
	if err != nil {
		return err
	}
	if _, err := fw.Write(jpegData); err != nil {
		return err
	}

	// A4 页面 EMU 尺寸 (按首页方向)
	pgW, pgH := a4WTwips*emuPerTwip, a4HTwips*emuPerTwip
	if !b.portrait {
		pgW, pgH = pgH, pgW
	}
	// 图片宽高撑满页宽, 按 PDF 原始比例缩放
	ratio := pageHpt / pageWpt
	wEMU := pgW
	hEMU := int(float64(wEMU) * ratio)
	if hEMU > pgH {
		hEMU = pgH
		wEMU = int(float64(hEMU) / ratio)
	}
	b.pages = append(b.pages, pageItem{relID: fmt.Sprintf("rId%d", b.mediaN), wEMU: wEMU, hEMU: hEMU})
	return nil
}

func (b *docxBuilder) close() error {
	// 固定部件
	if err := b.writeFile("[Content_Types].xml", b.contentTypesXML()); err != nil {
		return err
	}
	if err := b.writeFile("_rels/.rels", b.rootRelsXML()); err != nil {
		return err
	}
	if err := b.writeFile("docProps/core.xml", b.coreXML()); err != nil {
		return err
	}
	if err := b.writeFile("docProps/app.xml", b.appXML()); err != nil {
		return err
	}
	if err := b.writeFile("word/styles.xml", b.stylesXML()); err != nil {
		return err
	}
	if err := b.writeFile("word/_rels/document.xml.rels", b.docRelsXML()); err != nil {
		return err
	}
	// document.xml 最后写
	if err := b.writeFile("word/document.xml", b.documentXML()); err != nil {
		return err
	}
	return b.zip.Close()
}

func (b *docxBuilder) writeFile(name, content string) error {
	fw, err := b.zip.Create(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(fw, content)
	return err
}

func (b *docxBuilder) contentTypesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="jpeg" ContentType="image/jpeg"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`
}

func (b *docxBuilder) rootRelsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`
}

func (b *docxBuilder) coreXML() string {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:creator>pdf2word</dc:creator><cp:lastModifiedBy>pdf2word</cp:lastModifiedBy><dcterms:created xsi:type="dcterms:W3CDTF">` + now + `</dcterms:created><dcterms:modified xsi:type="dcterms:W3CDTF">` + now + `</dcterms:modified></cp:coreProperties>`
}

func (b *docxBuilder) appXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>pdf2word</Application><Pages>` + fmt.Sprint(len(b.pages)) + `</Pages></Properties>`
}

func (b *docxBuilder) stylesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr/></w:rPrDefault><w:pPrDefault><w:pPr/></w:pPrDefault></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style></w:styles>`
}

func (b *docxBuilder) docRelsXML() string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	sb.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	sb.WriteString(`<Relationship Id="rIdStyles" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`)
	for i, p := range b.pages {
		sb.WriteString(fmt.Sprintf(`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image%d.jpeg"/>`, p.relID, i+1))
	}
	sb.WriteString(`</Relationships>`)
	return sb.String()
}

func (b *docxBuilder) documentXML() string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	sb.WriteString(`<w:document xmlns:wpc="http://schemas.microsoft.com/office/word/2010/wordprocessingCanvas" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:o="urn:schemas-microsoft-com:office:office" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math" xmlns:v="urn:schemas-microsoft-com:vml" xmlns:wp14="http://schemas.microsoft.com/office/word/2010/wordprocessingDrawing" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:w10="urn:schemas-microsoft-com:office:word" xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:w14="http://schemas.microsoft.com/office/word/2010/wordml" xmlns:w15="http://schemas.microsoft.com/office/word/2012/wordml" xmlns:wpg="http://schemas.microsoft.com/office/word/2010/wordprocessingGroup" xmlns:wpi="http://schemas.microsoft.com/office/word/2010/wordprocessingInk" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:body>`)

	for i, p := range b.pages {
		// 除第一页外, 每页前强制分页
		pageBreak := ""
		if i > 0 {
			pageBreak = `<w:pageBreakBefore/>`
		}
		sb.WriteString(`<w:p><w:pPr>` + pageBreak + `<w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="` +
			fmt.Sprint(p.wEMU) + `" cy="` + fmt.Sprint(p.hEMU) + `"/><wp:effectExtent l="0" t="0" r="0" b="0"/><wp:docPr id="` + fmt.Sprint(i+1) + `" name="Picture ` + fmt.Sprint(i+1) + `"/><wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="` + fmt.Sprint(i+1) + `" name="image` + fmt.Sprint(i+1) + `.jpeg"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="` + p.relID + `"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="` + fmt.Sprint(p.wEMU) + `" cy="` + fmt.Sprint(p.hEMU) + `"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`)
	}

	// 节属性: A4, 零边距, 方向跟随首页
	pgW, pgH := a4WTwips, a4HTwips
	orient := ""
	if !b.portrait {
		pgW, pgH = a4HTwips, a4WTwips
		orient = `<w:pgSz w:w="` + fmt.Sprint(pgW) + `" w:h="` + fmt.Sprint(pgH) + `" w:orient="landscape"/>`
	} else {
		orient = `<w:pgSz w:w="` + fmt.Sprint(pgW) + `" w:h="` + fmt.Sprint(pgH) + `"/>`
	}
	sb.WriteString(`<w:sectPr>` + orient + `<w:pgMar w:top="0" w:right="0" w:bottom="0" w:left="0" w:header="0" w:footer="0" w:gutter="0"/><w:cols w:space="425"/><w:docGrid w:linePitch="312"/></w:sectPr>`)
	sb.WriteString(`</w:body></w:document>`)
	return sb.String()
}

// ======================= 主流程 =======================
// 平台库加载实现见 load_unix.go (dlopen) / load_windows.go (LoadLibraryA)

func convertOne(pdfPath, outPath string, dpi, quality int) (pages int, sizeMB float64, err error) {
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return 0, 0, err
	}
	if len(data) == 0 {
		return 0, 0, fmt.Errorf("空文件")
	}

	// 文档只加载一次, 全程复用
	// pdfiumMu.Lock() // 实验: 暂时关闭
	doc := fnLoadMemDoc(unsafe.Pointer(&data[0]), int32(len(data)), nil)
	if doc == nil {
		// pdfiumMu.Unlock()
		return 0, 0, fmt.Errorf("PDFium 无法打开文档（文件损坏或加密）")
	}
	n := int(fnGetPageCount(doc))
	p0 := fnLoadPage(doc, 0)
	if p0 == nil {
		fnCloseDoc(doc)
		// pdfiumMu.Unlock()
		return 0, 0, fmt.Errorf("无法打开第一页")
	}
	p0w, p0h := fnGetPageWidth(p0), fnGetPageHeight(p0)
	fnClosePage(p0)
	// pdfiumMu.Unlock()

	f, err := os.Create(outPath)
	if err != nil {
		fnCloseDoc(doc)
		return 0, 0, err
	}
	defer func() {
		f.Close()
		fnCloseDoc(doc)
		// 关键: PDFium 全程持有 &data[0] 指针, 但 Go 代码里 data 在 LoadMemDoc 后不再被引用,
		// GC 可能在循环中途回收 data 底层数组, 导致后续页解析失败. KeepAlive 强制保活到文档关闭.
		runtime.KeepAlive(data)
	}()

	portrait := p0h >= p0w
	builder, err := newDocxBuilder(f, portrait)
	if err != nil {
		return 0, 0, err
	}
	for i := 0; i < n; i++ {
		jpg, pw, ph, err := renderPage(doc, i, dpi, quality)
		if err != nil {
			return 0, 0, fmt.Errorf("第 %d 页: %v", i+1, err)
		}
		if err := builder.addPage(jpg, pw, ph); err != nil {
			return 0, 0, err
		}
	}
	if err := builder.close(); err != nil {
		return 0, 0, err
	}
	st, _ := os.Stat(outPath)
	return n, float64(st.Size()) / 1024 / 1024, nil
}

func main() {
	// 积极 GC, 压低内存足迹 (低内存机器更稳)
	// debug.SetGCPercent(30) // 实验: 暂时关闭排查问题

	var (
		recursive = flag.Bool("r", false, "递归子目录")
		dpi       = flag.Int("dpi", 150, "渲染 DPI (默认150)")
		quality   = flag.Int("q", 85, "JPEG 质量 1-100 (默认85)")
		outDir    = flag.String("o", "", "输出目录 (默认与 PDF 同目录)")
		force     = flag.Bool("f", false, "覆盖已存在的 docx")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "pdf2word — PDF 转图片型 Word (每页一张图, 不 OCR, 不编辑)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "用法:")
		fmt.Fprintln(os.Stderr, "  pdf2word.exe                转换当前目录所有 PDF")
		fmt.Fprintln(os.Stderr, "  pdf2word.exe <目录>         转换指定目录 (可把文件夹拖到 exe 上)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "选项:")
		flag.PrintDefaults()
	}
	flag.Parse()

	// 目标目录
	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		fmt.Printf("[错误] 目录不存在: %s\n", dir)
		os.Exit(1)
	}
	absDir, _ := filepath.Abs(dir)

	fmt.Printf("pdf2word 启动  目录: %s\n", absDir)
	if *recursive {
		fmt.Println("模式: 递归子目录")
	}
	fmt.Printf("参数: DPI=%d  JPEG质量=%d\n", *dpi, *quality)
	fmt.Println("--------------------------------------------------")

	if err := loadPdfium(); err != nil {
		fmt.Printf("[错误] %v\n", err)
		fmt.Println("提示: Windows 用户请把 pdfium.dll 放到本 exe 同目录")
		os.Exit(1)
	}
	defer fnDestroyLib()

	// 收集 PDF
	var pdfs []string
	walk := filepath.WalkDir
	_ = walk(absDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if !*recursive && path != absDir {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".pdf" {
			pdfs = append(pdfs, path)
		}
		return nil
	})
	sort.Strings(pdfs)

	if len(pdfs) == 0 {
		fmt.Println("[完成] 该目录下没有 PDF 文件")
		return
	}

	okCount, failCount := 0, 0
	t0 := time.Now()
	for i, pdfPath := range pdfs {
		rel, _ := filepath.Rel(absDir, pdfPath)
		outPath := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath)) + ".docx"
		if *outDir != "" {
			os.MkdirAll(*outDir, 0o755)
			outPath = filepath.Join(*outDir, strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath))+".docx")
		}
		if !*force {
			if _, err := os.Stat(outPath); err == nil {
				fmt.Printf("[%d/%d] 跳过(已存在): %s\n", i+1, len(pdfs), rel)
				okCount++
				continue
			}
		}
		fmt.Printf("[%d/%d] 转换中: %s ... ", i+1, len(pdfs), rel)
		pages, sizeMB, err := convertOne(pdfPath, outPath, *dpi, *quality)
		if err != nil {
			fmt.Printf("失败: %v\n", err)
			os.Remove(outPath) // 清残留
			failCount++
			continue
		}
		fmt.Printf("完成  %d 页, %.1f MB -> %s\n", pages, sizeMB, filepath.Base(outPath))
		okCount++
	}
	fmt.Println("--------------------------------------------------")
	fmt.Printf("===== 汇总: 成功 %d, 失败 %d, 总耗时 %.1f 秒 =====\n", okCount, failCount, time.Since(t0).Seconds())
	if failCount > 0 {
		os.Exit(2)
	}
}
