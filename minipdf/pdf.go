// Package minipdf is a small PDF writer: A4 portrait pages, the standard Helvetica and
// Courier faces (WinAnsi-encoded text), vector drawing and link annotations.
// Units are millimetres with the origin at the top-left corner of the page.
package minipdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"os"
	"strings"
	"unicode/utf16"
)

const (
	pageW   = 210.0
	pageH   = 297.0
	ptPerMM = 72.0 / 25.4
	kappa   = 0.5522847498 // bezier circle approximation
)

// Point is a position in millimetres.
type Point struct{ X, Y float64 }

type annot struct {
	x1, y1, x2, y2 float64 // PDF points, origin bottom-left
	uri            string
}

type page struct {
	buf    bytes.Buffer
	annots []annot
}

type state struct {
	family                          string
	bold, italic, underline         bool
	size                            float64
	textColor, drawColor, fillColor [3]int
	lineWidth                       float64
	dash                            []float64
	dashPhase                       float64
	x, y                            float64
	autoBreak                       bool
}

// Doc is a PDF document under construction.
type Doc struct {
	state
	pages       []*page
	cur         *page
	lm, tm, rm  float64
	breakMargin float64
	footer      func()
	inFooter    bool
	closed      bool
	title       string
	fonts       []string
	fontIdx     map[string]int
}

// New returns an empty document; call AddPage before drawing.
func New() *Doc {
	return &Doc{
		state: state{
			family:    "helvetica",
			size:      12,
			lineWidth: 0.2,
			autoBreak: true,
		},
		lm: 10, tm: 10, rm: 10,
		breakMargin: 20,
		fontIdx:     map[string]int{},
	}
}

func (d *Doc) SetMargins(left, top, right float64) {
	d.tm, d.rm = top, right
	d.SetLeftMargin(left)
}

func (d *Doc) SetLeftMargin(m float64) {
	d.lm = m
	if d.cur != nil && d.x < m {
		d.x = m
	}
}

// GetMargins returns the left, top and right margins and the bottom page-break margin.
func (d *Doc) GetMargins() (left, top, right, bottom float64) {
	return d.lm, d.tm, d.rm, d.breakMargin
}

func (d *Doc) GetPageSize() (w, h float64) { return pageW, pageH }

func (d *Doc) SetAutoPageBreak(auto bool, margin float64) {
	d.autoBreak, d.breakMargin = auto, margin
}

func (d *Doc) SetTitle(title string)  { d.title = title }
func (d *Doc) SetFooterFunc(f func()) { d.footer = f }
func (d *Doc) PageNo() int            { return len(d.pages) }

func (d *Doc) GetX() float64 { return d.x }
func (d *Doc) GetY() float64 { return d.y }

// SetX sets the horizontal position; a negative value is measured from the right edge.
func (d *Doc) SetX(x float64) {
	if x < 0 {
		x += pageW
	}
	d.x = x
}

// SetY sets the vertical position and resets x to the left margin; a negative value is measured from the bottom edge.
func (d *Doc) SetY(y float64) {
	if y < 0 {
		y += pageH
	}
	d.x, d.y = d.lm, y
}

func (d *Doc) SetXY(x, y float64) {
	d.SetY(y)
	d.SetX(x)
}

func (d *Doc) SetTextColor(r, g, b int) { d.textColor = [3]int{r, g, b} }
func (d *Doc) SetDrawColor(r, g, b int) { d.drawColor = [3]int{r, g, b} }
func (d *Doc) SetFillColor(r, g, b int) { d.fillColor = [3]int{r, g, b} }
func (d *Doc) SetLineWidth(w float64)   { d.lineWidth = w }

// SetDashPattern sets the stroke dash lengths in mm; an empty slice gives solid lines.
func (d *Doc) SetDashPattern(dash []float64, phase float64) {
	d.dash, d.dashPhase = dash, phase
}

// SetFont selects "Helvetica" or "Courier"; style may combine B, I and U.
func (d *Doc) SetFont(family, style string, size float64) {
	d.family = "helvetica"
	if strings.EqualFold(family, "courier") {
		d.family = "courier"
	}
	style = strings.ToUpper(style)
	d.bold = strings.Contains(style, "B")
	d.italic = strings.Contains(style, "I")
	d.underline = strings.Contains(style, "U")
	d.size = size
}

func (d *Doc) fontName() string {
	name := "Helvetica"
	if d.family == "courier" {
		name = "Courier"
	}
	switch {
	case d.bold && d.italic:
		return name + "-BoldOblique"
	case d.bold:
		return name + "-Bold"
	case d.italic:
		return name + "-Oblique"
	}
	return name
}

func (d *Doc) fontNum() int {
	name := d.fontName()
	if n, ok := d.fontIdx[name]; ok {
		return n
	}
	d.fonts = append(d.fonts, name)
	d.fontIdx[name] = len(d.fonts)
	return len(d.fonts)
}

func (d *Doc) glyphWidth(c byte) int {
	switch {
	case d.family == "courier":
		return 600
	case d.bold:
		return helveticaBoldWidths[c]
	}
	return helveticaWidths[c]
}

func (d *Doc) fontMM() float64 { return d.size / ptPerMM }

// GetStringWidth returns the width in mm of WinAnsi-encoded text in the current font.
func (d *Doc) GetStringWidth(s string) float64 {
	w := 0
	for i := 0; i < len(s); i++ {
		w += d.glyphWidth(s[i])
	}
	return float64(w) * d.fontMM() / 1000
}

var cp1252Extra = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88,
	'‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E, '‘': 0x91, '’': 0x92, '“': 0x93,
	'”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97, '˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B,
	'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// Encode converts UTF-8 text to the single-byte WinAnsi encoding used by all text methods.
// Newlines are kept, other control characters become spaces and unsupported runes become '?'.
func Encode(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b = append(b, '\n')
		case r < 0x20:
			b = append(b, ' ')
		case r < 0x7F, r >= 0xA0 && r <= 0xFF:
			b = append(b, byte(r))
		default:
			if c, ok := cp1252Extra[r]; ok {
				b = append(b, c)
			} else {
				b = append(b, '?')
			}
		}
	}
	return string(b)
}

// AddPage finishes the current page (running the footer) and starts a new one.
func (d *Doc) AddPage() {
	if d.cur != nil {
		d.runFooter()
	}
	d.cur = &page{}
	d.pages = append(d.pages, d.cur)
	d.x, d.y = d.lm, d.tm
}

func (d *Doc) runFooter() {
	if d.footer == nil || d.inFooter {
		return
	}
	saved := d.state
	d.inFooter = true
	d.footer()
	d.inFooter = false
	d.state = saved
}

func (d *Doc) breakIfNeeded(h float64) {
	if d.autoBreak && !d.inFooter && d.y+h > pageH-d.breakMargin {
		x := d.x
		d.AddPage()
		d.x = x
	}
}

func (d *Doc) out(format string, args ...any) {
	if d.cur == nil {
		d.AddPage()
	}
	fmt.Fprintf(&d.cur.buf, format, args...)
	d.cur.buf.WriteByte('\n')
}

func px(x float64) float64 { return x * ptPerMM }
func py(y float64) float64 { return (pageH - y) * ptPerMM }

func rgb(c [3]int) string {
	return fmt.Sprintf("%.3f %.3f %.3f", float64(c[0])/255, float64(c[1])/255, float64(c[2])/255)
}

func (d *Doc) strokeState() {
	d.out("%s RG %.2f w", rgb(d.drawColor), px(d.lineWidth))
	if len(d.dash) == 0 {
		d.out("[] 0 d")
		return
	}
	parts := make([]string, len(d.dash))
	for i, v := range d.dash {
		parts[i] = fmt.Sprintf("%.2f", px(v))
	}
	d.out("[%s] %.2f d", strings.Join(parts, " "), px(d.dashPhase))
}

// paintState emits colour state for a D/F/DF style and returns the paint operator.
func (d *Doc) paintState(style string) string {
	style = strings.ToUpper(style)
	fill := strings.Contains(style, "F")
	stroke := strings.Contains(style, "D") || !fill
	if stroke {
		d.strokeState()
	}
	if fill {
		d.out("%s rg", rgb(d.fillColor))
	}
	switch {
	case stroke && fill:
		return "B"
	case fill:
		return "f"
	}
	return "S"
}

func (d *Doc) Line(x1, y1, x2, y2 float64) {
	d.strokeState()
	d.out("%.2f %.2f m %.2f %.2f l S", px(x1), py(y1), px(x2), py(y2))
}

// Rect draws a rectangle; style is "D" (outline), "F" (fill) or "DF".
func (d *Doc) Rect(x, y, w, h float64, style string) {
	op := d.paintState(style)
	d.out("%.2f %.2f %.2f %.2f re %s", px(x), py(y+h), px(w), px(h), op)
}

func (d *Doc) Polygon(pts []Point, style string) {
	if len(pts) < 2 {
		return
	}
	op := d.paintState(style)
	for i, p := range pts {
		cmd := "l"
		if i == 0 {
			cmd = "m"
		}
		d.out("%.2f %.2f %s", px(p.X), py(p.Y), cmd)
	}
	d.out("h %s", op)
}

func (d *Doc) Ellipse(cx, cy, rx, ry float64, style string) {
	op := d.paintState(style)
	x, y, a, b := px(cx), py(cy), px(rx), px(ry)
	k := kappa
	d.out("%.2f %.2f m", x+a, y)
	d.out("%.2f %.2f %.2f %.2f %.2f %.2f c", x+a, y+k*b, x+k*a, y+b, x, y+b)
	d.out("%.2f %.2f %.2f %.2f %.2f %.2f c", x-k*a, y+b, x-a, y+k*b, x-a, y)
	d.out("%.2f %.2f %.2f %.2f %.2f %.2f c", x-a, y-k*b, x-k*a, y-b, x, y-b)
	d.out("%.2f %.2f %.2f %.2f %.2f %.2f c", x+k*a, y-b, x+a, y-k*b, x+a, y)
	d.out("h %s", op)
}

// RoundRect draws a rectangle with all four corners rounded by radius r.
func (d *Doc) RoundRect(x, y, w, h, r float64, style string) {
	r = min(r, w/2, h/2)
	k := r * (1 - kappa)
	op := d.paintState(style)
	m := func(x, y float64) { d.out("%.2f %.2f m", px(x), py(y)) }
	l := func(x, y float64) { d.out("%.2f %.2f l", px(x), py(y)) }
	c := func(x1, y1, x2, y2, x3, y3 float64) {
		d.out("%.2f %.2f %.2f %.2f %.2f %.2f c", px(x1), py(y1), px(x2), py(y2), px(x3), py(y3))
	}
	m(x+r, y)
	l(x+w-r, y)
	c(x+w-k, y, x+w, y+k, x+w, y+r)
	l(x+w, y+h-r)
	c(x+w, y+h-k, x+w-k, y+h, x+w-r, y+h)
	l(x+r, y+h)
	c(x+k, y+h, x, y+h-k, x, y+h-r)
	l(x, y+r)
	c(x, y+k, x+k, y, x+r, y)
	d.out("h %s", op)
}

// BeginScale scales everything drawn until EndScale by s around the point (ox, oy).
func (d *Doc) BeginScale(s, ox, oy float64) {
	d.out("q %.5f 0 0 %.5f %.2f %.2f cm", s, s, px(ox)*(1-s), py(oy)*(1-s))
}

func (d *Doc) EndScale() { d.out("Q") }

func pdfString(s string) string {
	var sb strings.Builder
	sb.WriteByte('(')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', '(', ')':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case '\r':
			sb.WriteString(`\r`)
		case '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte(')')
	return sb.String()
}

// text draws WinAnsi text with its baseline at y.
func (d *Doc) text(x, y float64, s string) {
	d.out("BT /F%d %.2f Tf %s rg %.2f %.2f Td %s Tj ET", d.fontNum(), d.size, rgb(d.textColor), px(x), py(y), pdfString(s))
	if d.underline {
		pos := 0.1 * d.size
		d.out("%s rg %.2f %.2f %.2f %.2f re f", rgb(d.textColor), px(x), py(y)-pos, d.GetStringWidth(s)*ptPerMM, 0.05*d.size)
	}
}

// Cell draws text in a w x h box at the cursor and moves the cursor right by w.
// A zero w extends the box to the right margin; align is "L", "C" or "R".
func (d *Doc) Cell(w, h float64, txt, align string, fill bool) {
	d.breakIfNeeded(h)
	if w == 0 {
		w = pageW - d.rm - d.x
	}
	if fill {
		d.Rect(d.x, d.y, w, h, "F")
	}
	if txt != "" {
		x := d.x
		switch strings.ToUpper(align) {
		case "C":
			x += (w - d.GetStringWidth(txt)) / 2
		case "R":
			x += w - d.GetStringWidth(txt)
		}
		d.text(x, d.y+h/2+0.3*d.fontMM(), txt)
	}
	d.x += w
}

// Ln moves the cursor to the left margin of the next line of height h.
func (d *Doc) Ln(h float64) { d.x, d.y = d.lm, d.y+h }

// Write flows text from the cursor, wrapping at the right margin.
func (d *Doc) Write(h float64, txt string) { d.write(h, txt, "") }

// WriteLink is like Write but makes the text a link to uri.
func (d *Doc) WriteLink(h float64, txt, uri string) { d.write(h, txt, uri) }

func (d *Doc) write(h float64, txt, uri string) {
	for _, tok := range splitSpaces(txt) {
		for tok != "" {
			avail := pageW - d.rm - d.x
			if d.GetStringWidth(tok) <= avail+1e-6 {
				d.put(h, tok, uri)
				break
			}
			if tok[0] == ' ' {
				break
			}
			if d.x > d.lm+1e-6 {
				d.Ln(h)
				continue
			}
			// a single word wider than the line: split it where it fits
			n := 1
			for n < len(tok) && d.GetStringWidth(tok[:n+1]) <= avail {
				n++
			}
			d.put(h, tok[:n], uri)
			d.Ln(h)
			tok = tok[n:]
		}
	}
}

func (d *Doc) put(h float64, s, uri string) {
	d.breakIfNeeded(h)
	w := d.GetStringWidth(s)
	d.text(d.x, d.y+h/2+0.3*d.fontMM(), s)
	if uri != "" {
		d.cur.annots = append(d.cur.annots, annot{px(d.x), py(d.y + h), px(d.x + w), py(d.y), uri})
	}
	d.x += w
}

// splitSpaces splits s into alternating runs of spaces and non-spaces.
func splitSpaces(s string) []string {
	var toks []string
	start := 0
	for i := 1; i <= len(s); i++ {
		if i == len(s) || (s[i] == ' ') != (s[start] == ' ') {
			toks = append(toks, s[start:i])
			start = i
		}
	}
	if len(s) == 0 {
		return nil
	}
	return toks
}

// Save finishes the document and writes it to path.
func (d *Doc) Save(path string) error {
	data, err := d.Bytes()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Bytes finishes the document and returns the PDF file contents.
func (d *Doc) Bytes() ([]byte, error) {
	if len(d.pages) == 0 {
		d.AddPage()
	}
	if !d.closed {
		d.runFooter()
		d.closed = true
	}

	const fontBase = 4
	next := fontBase + len(d.fonts)
	pageObj := make([]int, len(d.pages))
	for i, p := range d.pages {
		pageObj[i] = next
		next += 2 + len(p.annots)
	}

	var buf bytes.Buffer
	var offs []int
	begin := func() {
		offs = append(offs, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n", len(offs))
	}
	obj := func(body string) {
		begin()
		fmt.Fprintf(&buf, "%s\nendobj\n", body)
	}

	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, len(pageObj))
	for i, n := range pageObj {
		kids[i] = fmt.Sprintf("%d 0 R", n)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pageObj)))
	obj(fmt.Sprintf("<< /Title %s /Producer (minipdf) >>", infoString(d.title)))
	for _, name := range d.fonts {
		obj(fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding /WinAnsiEncoding >>", name))
	}

	var res strings.Builder
	for i := range d.fonts {
		fmt.Fprintf(&res, "/F%d %d 0 R ", i+1, fontBase+i)
	}
	for i, p := range d.pages {
		annots := make([]string, len(p.annots))
		for j := range p.annots {
			annots[j] = fmt.Sprintf("%d 0 R", pageObj[i]+2+j)
		}
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /Font << %s>> >> /Contents %d 0 R /Annots [%s] >>",
			pageW*ptPerMM, pageH*ptPerMM, res.String(), pageObj[i]+1, strings.Join(annots, " ")))

		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write(p.buf.Bytes()); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		begin()
		fmt.Fprintf(&buf, "<< /Filter /FlateDecode /Length %d >>\nstream\n", z.Len())
		buf.Write(z.Bytes())
		buf.WriteString("\nendstream\nendobj\n")

		for _, a := range p.annots {
			obj(fmt.Sprintf("<< /Type /Annot /Subtype /Link /Rect [%.2f %.2f %.2f %.2f] /Border [0 0 0] /A << /S /URI /URI %s >> >>",
				a.x1, a.y1, a.x2, a.y2, pdfString(asciiURI(a.uri))))
		}
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info 3 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return buf.Bytes(), nil
}

// infoString encodes a document-info text string, using UTF-16BE when it is not plain ASCII.
func infoString(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x7F || s[i] < 0x20 {
			ascii = false
		}
	}
	if ascii {
		return pdfString(s)
	}
	var sb strings.Builder
	sb.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&sb, "%04X", u)
	}
	sb.WriteByte('>')
	return sb.String()
}

func asciiURI(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c > 0x20 && c < 0x7F {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}
