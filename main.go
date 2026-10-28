package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"markdown-converter/minipdf"
)

const (
	margin     = 20.0
	baseSize   = 11.0
	codeSize   = 9.0
	listIndent = 6.0
	codePad    = 2.0
)

var headingSizes = [...]float64{24, 20, 16, 14, 12, 11}

type style struct {
	bold, italic, code bool
	link               string
}

type renderer struct {
	pdf   *minipdf.Doc
	src   []byte
	tr    func(string) string
	size  float64
	color [3]int
}

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintf(os.Stderr, "usage: %s input.md [output.pdf]\n", filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	in := os.Args[1]
	out := strings.TrimSuffix(in, filepath.Ext(in)) + ".pdf"
	if len(os.Args) == 3 {
		out = os.Args[2]
	}
	if err := convert(in, out); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println("written", out)
}

func convert(in, out string) error {
	src, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	md := goldmark.New(goldmark.WithExtensions(extension.Table))
	doc := md.Parser().Parse(text.NewReader(src))

	pdf := minipdf.New()
	pdf.SetMargins(margin, margin, margin)
	pdf.SetAutoPageBreak(true, margin)
	pdf.SetTitle(strings.TrimSuffix(filepath.Base(in), filepath.Ext(in)))
	pdf.SetFooterFunc(func() {
		pdf.SetY(-15)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.Cell(0, 10, fmt.Sprint(pdf.PageNo()), "C", false)
	})
	pdf.AddPage()

	r := &renderer{
		pdf:   pdf,
		src:   src,
		tr:    minipdf.Encode,
		size:  baseSize,
		color: [3]int{30, 30, 30},
	}
	r.blocks(doc)
	return pdf.Save(out)
}

func (r *renderer) lh() float64 { return r.size * 0.5 }

func (r *renderer) blocks(parent ast.Node) {
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		r.block(c)
	}
}

func (r *renderer) block(n ast.Node) {
	pdf := r.pdf
	switch v := n.(type) {
	case *ast.Heading:
		saved := r.size
		r.size = headingSizes[min(v.Level, 6)-1]
		if pdf.GetY() > margin+1 {
			pdf.Ln(r.lh() * 0.6)
		}
		r.inline(v, style{bold: true})
		pdf.Ln(r.lh() * 1.3)
		r.size = saved

	case *ast.Paragraph:
		r.inline(v, style{})
		pdf.Ln(r.lh() * 1.6)

	case *ast.TextBlock:
		r.inline(v, style{})
		pdf.Ln(r.lh())

	case *ast.List:
		r.list(v)

	case *ast.FencedCodeBlock, *ast.CodeBlock:
		r.codeBlock(n)

	case *ast.Blockquote:
		r.blockquote(v)

	case *ast.ThematicBreak:
		pdf.Ln(r.lh() * 0.5)
		pdf.SetDrawColor(180, 180, 180)
		y := pdf.GetY()
		pdf.Line(pdf.GetX(), y, 210-margin, y)
		pdf.Ln(r.lh())

	case *extast.Table:
		r.table(v)
		pdf.Ln(r.lh() * 0.6)

	default:
		if n.HasChildren() {
			r.blocks(n)
		}
	}
}

func (r *renderer) list(l *ast.List) {
	pdf := r.pdf
	lm, _, _, _ := pdf.GetMargins()
	num := l.Start
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		marker := "-"
		if l.IsOrdered() {
			marker = fmt.Sprintf("%d%c", num, l.Marker)
			num++
		}
		r.setFont(style{})
		pdf.SetX(lm)
		pdf.Cell(listIndent, r.lh(), r.tr(marker), "L", false)
		pdf.SetLeftMargin(lm + listIndent)
		pdf.SetX(lm + listIndent)
		r.blocks(item)
		pdf.SetLeftMargin(lm)
	}
	if _, ok := l.Parent().(*ast.ListItem); !ok {
		pdf.Ln(r.lh() * 0.5)
	}
}

func (r *renderer) codeBlock(n ast.Node) {
	pdf := r.pdf
	if f, ok := n.(*ast.FencedCodeBlock); ok && string(f.Language(r.src)) == "mermaid" {
		var sb strings.Builder
		for i := 0; i < n.Lines().Len(); i++ {
			seg := n.Lines().At(i)
			sb.Write(seg.Value(r.src))
		}
		if fc, ok := parseFlowchart(sb.String()); ok {
			r.flowchart(fc)
			return
		}
		if sd, ok := parseSequence(sb.String()); ok {
			r.sequence(sd)
			return
		}
	}
	pdf.Ln(r.lh() * 0.2)
	pdf.SetFont("Courier", "", codeSize)
	pdf.SetFillColor(240, 240, 240)
	pdf.SetTextColor(40, 40, 40)
	_, _, rm, _ := pdf.GetMargins()
	pageW, pageH := pdf.GetPageSize()
	boxW, lineH := pageW-rm-pdf.GetX(), codeSize*0.45
	perLine := max(1, int((boxW-2*codePad)/pdf.GetStringWidth("M")))
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		line := strings.TrimRight(string(seg.Value(r.src)), "\r\n")
		line = r.tr(strings.ReplaceAll(line, "\t", "    "))
		for {
			part := line[:min(perLine, len(line))]
			line = line[len(part):]
			if pdf.GetY()+lineH > pageH-margin {
				pdf.AddPage()
				pdf.SetFont("Courier", "", codeSize)
				pdf.SetFillColor(240, 240, 240)
				pdf.SetTextColor(40, 40, 40)
			}
			x, y := pdf.GetX(), pdf.GetY()
			pdf.Rect(x, y, boxW, lineH, "F")
			pdf.SetXY(x+codePad, y)
			pdf.Cell(boxW-2*codePad, lineH, part, "L", false)
			pdf.SetXY(x, y+lineH)
			if line == "" {
				break
			}
		}
	}
	pdf.Ln(r.lh() * 0.8)
}

func (r *renderer) blockquote(b *ast.Blockquote) {
	pdf := r.pdf
	lm, _, _, _ := pdf.GetMargins()
	page, startY := pdf.PageNo(), pdf.GetY()
	savedColor := r.color
	r.color = [3]int{100, 100, 100}
	pdf.SetLeftMargin(lm + listIndent)
	pdf.SetX(lm + listIndent)
	r.blocks(b)
	pdf.SetLeftMargin(lm)
	pdf.SetX(lm)
	r.color = savedColor
	if pdf.PageNo() == page {
		pdf.SetDrawColor(190, 190, 190)
		pdf.SetLineWidth(0.8)
		pdf.Line(lm+1.5, startY, lm+1.5, pdf.GetY()-r.lh()*0.6)
		pdf.SetLineWidth(0.2)
	}
}

func (r *renderer) table(t *extast.Table) {
	pdf := r.pdf
	const pad = 1.5
	lm, _, rm, _ := pdf.GetMargins()
	pageW, pageH := pdf.GetPageSize()
	cols := 0
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		cols = max(cols, row.ChildCount())
	}
	if cols == 0 {
		return
	}
	colW := (pageW - lm - rm) / float64(cols)
	lineH := baseSize * 0.45
	pdf.SetDrawColor(160, 160, 160)

	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		_, isHeader := row.(*extast.TableHeader)
		st := style{bold: isHeader}
		r.setFont(st)

		var cells [][]string
		rowLines := 1
		for c := row.FirstChild(); c != nil; c = c.NextSibling() {
			lines := r.wrap(r.tr(r.plainText(c)), colW-2*pad)
			cells = append(cells, lines)
			rowLines = max(rowLines, len(lines))
		}
		h := float64(rowLines)*lineH + 2*pad
		if pdf.GetY()+h > pageH-margin {
			pdf.AddPage()
			r.setFont(st)
		}
		x, y := lm, pdf.GetY()
		for _, lines := range cells {
			style := "D"
			if isHeader {
				pdf.SetFillColor(230, 230, 230)
				style = "DF"
			}
			pdf.Rect(x, y, colW, h, style)
			for i, ln := range lines {
				pdf.SetXY(x+pad, y+pad+float64(i)*lineH)
				pdf.Cell(colW-2*pad, lineH, ln, "L", false)
			}
			x += colW
		}
		pdf.SetXY(lm, y+h)
	}
}

// wrap splits CP1252-encoded text (single bytes) into lines that fit width w.
func (r *renderer) wrap(s string, w float64) []string {
	var lines []string
	cur := ""
	flush := func() {
		lines = append(lines, cur)
		cur = ""
	}
	for _, word := range strings.Fields(s) {
		for r.pdf.GetStringWidth(word) > w {
			// break overlong word at the last byte that fits
			if cur != "" {
				flush()
			}
			n := 1
			for n < len(word) && r.pdf.GetStringWidth(word[:n+1]) <= w {
				n++
			}
			cur = word[:n]
			word = word[n:]
			flush()
		}
		switch {
		case cur == "":
			cur = word
		case r.pdf.GetStringWidth(cur+" "+word) <= w:
			cur += " " + word
		default:
			flush()
			cur = word
		}
	}
	if cur != "" || len(lines) == 0 {
		flush()
	}
	return lines
}

func (r *renderer) plainText(n ast.Node) string {
	var sb strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			sb.Write(v.Segment.Value(r.src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				sb.WriteByte(' ')
			}
		case *ast.String:
			sb.Write(v.Value)
		default:
			sb.WriteString(r.plainText(c))
		}
	}
	return sb.String()
}

func (r *renderer) setFont(st style) {
	family, fs, size := "Helvetica", "", r.size
	if st.code {
		family, size = "Courier", r.size-1
	}
	if st.bold {
		fs += "B"
	}
	if st.italic {
		fs += "I"
	}
	col := r.color
	if st.link != "" {
		fs += "U"
		col = [3]int{0, 80, 200}
	} else if st.code {
		col = [3]int{160, 30, 60}
	}
	r.pdf.SetFont(family, fs, size)
	r.pdf.SetTextColor(col[0], col[1], col[2])
}

func (r *renderer) write(s string, st style) {
	if s == "" {
		return
	}
	r.setFont(st)
	if st.link != "" {
		r.pdf.WriteLink(r.lh(), r.tr(s), st.link)
		return
	}
	r.pdf.Write(r.lh(), r.tr(s))
}

func (r *renderer) inline(n ast.Node, st style) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			r.write(string(v.Segment.Value(r.src)), st)
			if v.HardLineBreak() {
				r.pdf.Ln(r.lh())
			} else if v.SoftLineBreak() {
				r.write(" ", st)
			}
		case *ast.String:
			r.write(string(v.Value), st)
		case *ast.Emphasis:
			s := st
			if v.Level >= 2 {
				s.bold = true
			} else {
				s.italic = true
			}
			r.inline(v, s)
		case *ast.CodeSpan:
			s := st
			s.code = true
			r.inline(v, s)
		case *ast.Link:
			s := st
			s.link = string(v.Destination)
			r.inline(v, s)
		case *ast.AutoLink:
			s := st
			s.link = string(v.URL(r.src))
			r.write(string(v.Label(r.src)), s)
		default:
			if c.HasChildren() {
				r.inline(c, st)
			}
		}
	}
}
