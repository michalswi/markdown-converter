package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"markdown-converter/minipdf"
)

const (
	seqFont    = 8.5
	seqLine    = 3.8
	seqMsgMax  = 55.0
	seqNoteMax = 45.0
	seqHead    = 2.4
)

type seqActor struct {
	id, label string
	lines     []string
	w, x      float64 // x is the lifeline position
}

type seqItem struct {
	kind     string // msg, note, open, else, close
	from, to int
	side     string // over, left, right (notes)
	text     string
	num      int
	lines    []string
	tw       float64
	dashed   bool
	head     string // filled, none, cross, async
	frame    int
	h, y     float64
	chunk    int
}

type seqFrame struct {
	kind, label string
	depth       int
	start, end  int
	dividers    []int
	items       []int
	hidden      bool
	left, right float64
}

type sequence struct {
	actors     []*seqActor
	idx        map[string]int
	items      []*seqItem
	frames     []*seqFrame
	autonumber bool
}

var (
	seqActorRe = regexp.MustCompile(`^(participant|actor)\s+([A-Za-z0-9_]+)(?:\s+as\s+(.+))?$`)
	seqMsgRe   = regexp.MustCompile(`^([A-Za-z0-9_]+)\s*(-->>|->>|-->|->|--x|-x|--\)|-\))\s*[+-]?\s*([A-Za-z0-9_]+)\s*:\s*(.*)$`)
	seqNoteRe  = regexp.MustCompile(`(?i)^note\s+(over|left of|right of)\s+([A-Za-z0-9_]+)(?:\s*,\s*([A-Za-z0-9_]+))?\s*:\s*(.*)$`)
	seqOpenRe  = regexp.MustCompile(`^(loop|alt|opt|par|critical|break|rect)(?:\s+(.*))?$`)
	seqElseRe  = regexp.MustCompile(`^(else|and|option)(?:\s+(.*))?$`)
)

// parseSequence returns false when the code is not a sequence diagram or uses unsupported syntax.
func parseSequence(code string) (*sequence, bool) {
	sd := &sequence{idx: map[string]int{}}
	header := false
	var stack []int

	actor := func(id string) int {
		if i, ok := sd.idx[id]; ok {
			return i
		}
		sd.idx[id] = len(sd.actors)
		sd.actors = append(sd.actors, &seqActor{id: id, label: id})
		return len(sd.actors) - 1
	}
	add := func(it *seqItem) {
		sd.items = append(sd.items, it)
		for _, fi := range stack {
			sd.frames[fi].items = append(sd.frames[fi].items, len(sd.items)-1)
		}
	}

	for _, ln := range strings.Split(code, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "%%") {
			continue
		}
		if !header {
			if ln != "sequenceDiagram" {
				return nil, false
			}
			header = true
			continue
		}

		if m := seqActorRe.FindStringSubmatch(ln); m != nil {
			i := actor(m[2])
			if m[3] != "" {
				sd.actors[i].label = m[3]
			}
			continue
		}
		if m := seqMsgRe.FindStringSubmatch(ln); m != nil {
			it := &seqItem{kind: "msg", from: actor(m[1]), to: actor(m[3]), text: m[4]}
			it.dashed = strings.HasPrefix(m[2], "--")
			switch strings.TrimLeft(m[2], "-") {
			case ">>":
				it.head = "filled"
			case ">":
				it.head = "none"
			case "x":
				it.head = "cross"
			default:
				it.head = "async"
			}
			add(it)
			continue
		}
		if m := seqNoteRe.FindStringSubmatch(ln); m != nil {
			it := &seqItem{kind: "note", from: actor(m[2]), text: m[4]}
			it.to = it.from
			if m[3] != "" {
				it.to = actor(m[3])
			}
			it.side = strings.Fields(strings.ToLower(m[1]))[0]
			add(it)
			continue
		}
		if m := seqOpenRe.FindStringSubmatch(ln); m != nil {
			f := &seqFrame{kind: m[1], label: m[2], depth: len(stack), start: -1, end: -1, hidden: m[1] == "rect"}
			sd.frames = append(sd.frames, f)
			if !f.hidden {
				sd.items = append(sd.items, &seqItem{kind: "open", frame: len(sd.frames) - 1})
				f.start = len(sd.items) - 1
			}
			stack = append(stack, len(sd.frames)-1)
			continue
		}
		if m := seqElseRe.FindStringSubmatch(ln); m != nil {
			if len(stack) == 0 {
				return nil, false
			}
			fi := stack[len(stack)-1]
			if f := sd.frames[fi]; !f.hidden {
				sd.items = append(sd.items, &seqItem{kind: "else", frame: fi, text: m[2]})
				f.dividers = append(f.dividers, len(sd.items)-1)
			}
			continue
		}
		if ln == "end" {
			if len(stack) == 0 {
				return nil, false
			}
			fi := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if f := sd.frames[fi]; !f.hidden {
				sd.items = append(sd.items, &seqItem{kind: "close", frame: fi})
				f.end = len(sd.items) - 1
			}
			continue
		}
		switch {
		case ln == "autonumber":
			sd.autonumber = true
		case strings.HasPrefix(ln, "activate "), strings.HasPrefix(ln, "deactivate "), strings.HasPrefix(ln, "title "):
		default:
			return nil, false
		}
	}
	return sd, header && len(stack) == 0 && len(sd.actors) > 0
}

func (r *renderer) seqLines(text string, maxW float64) ([]string, float64) {
	var lines []string
	tw := 0.0
	text = r.tr(brTag.ReplaceAllString(text, "\n"))
	for _, word := range strings.Fields(text) {
		maxW = math.Max(maxW, r.pdf.GetStringWidth(word))
	}
	for _, part := range strings.Split(text, "\n") {
		lines = append(lines, r.wrap(part, maxW)...)
	}
	for _, l := range lines {
		tw = math.Max(tw, r.pdf.GetStringWidth(l))
	}
	return lines, tw
}

// measure computes text sizes and row heights and returns the participant header height.
func (sd *sequence) measure(r *renderer) float64 {
	r.pdf.SetFont("Helvetica", "B", 9)
	hdr := 0.0
	for _, a := range sd.actors {
		var tw float64
		a.lines, tw = r.seqLines(a.label, 34)
		a.w = math.Max(tw+8, 20)
		hdr = math.Max(hdr, float64(len(a.lines))*4.2+4)
	}

	r.pdf.SetFont("Helvetica", "", seqFont)
	n := 0
	for _, it := range sd.items {
		switch it.kind {
		case "msg":
			text := it.text
			if sd.autonumber {
				n++
				text = fmt.Sprintf("%d. %s", n, text)
			}
			it.lines, it.tw = r.seqLines(text, seqMsgMax)
			it.h = float64(len(it.lines))*seqLine + 5
			if it.from == it.to {
				it.h = math.Max(float64(len(it.lines))*seqLine+2, 7) + 3
			}
		case "note":
			it.lines, it.tw = r.seqLines(it.text, seqNoteMax)
			it.h = float64(len(it.lines))*seqLine + 7
		case "open":
			it.h = 7
		case "else":
			it.h = 7
		case "close":
			it.h = 3
		}
	}
	return hdr
}

func (sd *sequence) noteBox(it *seqItem) (left, w float64) {
	boxW := it.tw + 6
	xf, xt := sd.actors[it.from].x, sd.actors[it.to].x
	switch it.side {
	case "right":
		return xf + 4, boxW
	case "left":
		return xf - 4 - boxW, boxW
	}
	if it.from == it.to {
		w = math.Max(boxW, 24)
		return xf - w/2, w
	}
	a, b := math.Min(xf, xt), math.Max(xf, xt)
	return a - 8, b - a + 16
}

func (sd *sequence) extent(it *seqItem) (float64, float64) {
	xf, xt := sd.actors[it.from].x, sd.actors[it.to].x
	if it.kind == "note" {
		l, w := sd.noteBox(it)
		return l, l + w
	}
	if it.from == it.to {
		return xf, xf + 10 + it.tw
	}
	return math.Min(xf, xt), math.Max(xf, xt)
}

// layoutX positions the lifelines and frames and returns the total width.
func (sd *sequence) layoutX() float64 {
	n := len(sd.actors)
	gaps := make([]float64, max(n-1, 0))
	for i := range gaps {
		gaps[i] = (sd.actors[i].w+sd.actors[i+1].w)/2 + 6
	}
	type constraint struct {
		a, b int
		w    float64
	}
	var cs []constraint
	needL, needR := 0.0, 0.0
	add := func(a, b int, w float64) { cs = append(cs, constraint{a, b, w}) }

	for _, it := range sd.items {
		switch it.kind {
		case "msg":
			if it.from != it.to {
				add(min(it.from, it.to), max(it.from, it.to), it.tw+8)
			} else if it.from < n-1 {
				add(it.from, it.from+1, it.tw+14)
			} else {
				needR = math.Max(needR, it.tw+12)
			}
		case "note":
			a := it.from
			boxW := it.tw + 6
			switch {
			case it.side == "right":
				if a < n-1 {
					add(a, a+1, boxW+8)
				} else {
					needR = math.Max(needR, boxW+4)
				}
			case it.side == "left":
				if a > 0 {
					add(a-1, a, boxW+8)
				} else {
					needL = math.Max(needL, boxW+4)
				}
			case it.from != it.to:
				add(min(it.from, it.to), max(it.from, it.to), boxW-16)
			default:
				bw := math.Max(boxW, 24)
				if a < n-1 {
					add(a, a+1, bw/2+4)
				} else {
					needR = math.Max(needR, bw/2)
				}
				if a > 0 {
					add(a-1, a, bw/2+4)
				} else {
					needL = math.Max(needL, bw/2)
				}
			}
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].b-cs[i].a < cs[j].b-cs[j].a })
	for _, c := range cs {
		sum := 0.0
		for i := c.a; i < c.b; i++ {
			sum += gaps[i]
		}
		if sum < c.w {
			extra := (c.w - sum) / float64(c.b-c.a)
			for i := c.a; i < c.b; i++ {
				gaps[i] += extra
			}
		}
	}

	x := math.Max(sd.actors[0].w/2, needL)
	for i, a := range sd.actors {
		a.x = x
		if i < n-1 {
			x += gaps[i]
		}
	}
	last := sd.actors[n-1]
	total := last.x + math.Max(last.w/2, needR)

	for _, f := range sd.frames {
		if f.hidden {
			continue
		}
		l, rgt := sd.actors[0].x, last.x
		if len(f.items) > 0 {
			l, rgt = math.Inf(1), math.Inf(-1)
			for _, i := range f.items {
				il, ir := sd.extent(sd.items[i])
				l, rgt = math.Min(l, il), math.Max(rgt, ir)
			}
		}
		pad := math.Max(4, 10-2*float64(f.depth))
		f.left, f.right = l-pad, rgt+pad
		total = math.Max(total, f.right+1)
	}
	return total
}

func (r *renderer) sequence(sd *sequence) {
	pdf := r.pdf
	hdr := sd.measure(r)
	W := sd.layoutX()

	lm, _, rm, _ := pdf.GetMargins()
	pageW, pageH := pdf.GetPageSize()
	availW := pageW - lm - rm
	s := math.Min(1, availW/W)

	pdf.Ln(r.lh() * 0.5)
	if pageH-margin-pdf.GetY() < 60 {
		pdf.AddPage()
	}
	rowsTop := hdr + 3
	capFirst := (pageH - margin - pdf.GetY()) / s
	capNext := (pageH - 2*margin) / s

	// Split rows across pages; each page repeats the participant header.
	var bottoms []float64
	chunk, cur := 0, rowsTop
	for _, it := range sd.items {
		limit := capFirst
		if chunk > 0 {
			limit = capNext
		}
		if cur+it.h > limit && cur > rowsTop {
			bottoms = append(bottoms, cur+2)
			chunk++
			cur = rowsTop
		}
		it.chunk, it.y = chunk, cur
		cur += it.h
	}
	bottoms = append(bottoms, cur+2)

	x0 := lm + (availW-W*s)/2
	y0 := 0.0
	pdf.SetAutoPageBreak(false, 0)
	for c, bottom := range bottoms {
		if c > 0 {
			pdf.AddPage()
		}
		y0 = pdf.GetY()
		pdf.BeginScale(s, x0, y0)
		r.seqChunk(sd, c, x0, y0, hdr, rowsTop, bottom)
		pdf.EndScale()
	}
	pdf.SetAutoPageBreak(true, margin)
	pdf.SetLineWidth(0.2)
	pdf.SetXY(lm, y0+bottoms[len(bottoms)-1]*s)
	pdf.Ln(r.lh() * 1.5)
}

func (r *renderer) seqText(x, y, w float64, lines []string, align string) {
	for k, l := range lines {
		r.pdf.SetXY(x, y+float64(k)*seqLine)
		r.pdf.Cell(w, seqLine, l, align, false)
	}
}

func (r *renderer) seqChunk(sd *sequence, c int, x0, y0, hdr, rowsTop, bottom float64) {
	pdf := r.pdf

	for _, f := range sd.frames {
		if f.hidden {
			continue
		}
		so, eo := sd.items[f.start], sd.items[f.end]
		if so.chunk > c || eo.chunk < c {
			continue
		}
		top, bot := rowsTop-1, bottom-2
		if so.chunk == c {
			top = so.y
		}
		if eo.chunk == c {
			bot = eo.y + eo.h - 1.5
		}
		pdf.SetDrawColor(140, 140, 140)
		pdf.SetLineWidth(0.3)
		pdf.Rect(x0+f.left, y0+top, f.right-f.left, bot-top, "D")
		if so.chunk == c {
			pdf.SetFont("Helvetica", "B", 7.5)
			kind := r.tr(f.kind)
			tabW := pdf.GetStringWidth(kind) + 4
			pdf.SetFillColor(235, 235, 235)
			pdf.Rect(x0+f.left, y0+top, tabW, 4.5, "DF")
			pdf.SetTextColor(30, 30, 30)
			pdf.SetXY(x0+f.left, y0+top+0.25)
			pdf.Cell(tabW, 4, kind, "C", false)
			if f.label != "" {
				pdf.SetFont("Helvetica", "I", seqFont)
				pdf.SetXY(x0+f.left+tabW+2, y0+top+0.25)
				pdf.Cell(0, 4, r.tr("["+f.label+"]"), "L", false)
			}
		}
		for _, d := range f.dividers {
			di := sd.items[d]
			if di.chunk != c {
				continue
			}
			pdf.SetDrawColor(140, 140, 140)
			pdf.SetDashPattern([]float64{1.5, 1.2}, 0)
			pdf.Line(x0+f.left, y0+di.y+0.5, x0+f.right, y0+di.y+0.5)
			pdf.SetDashPattern([]float64{}, 0)
			if di.text != "" {
				pdf.SetFont("Helvetica", "I", seqFont)
				pdf.SetTextColor(30, 30, 30)
				pdf.SetXY(x0+f.left+3, y0+di.y+1.5)
				pdf.Cell(0, 4, r.tr("["+di.text+"]"), "L", false)
			}
		}
	}

	pdf.SetDrawColor(170, 170, 170)
	pdf.SetLineWidth(0.25)
	pdf.SetDashPattern([]float64{1.5, 1.5}, 0)
	for _, a := range sd.actors {
		pdf.Line(x0+a.x, y0+hdr, x0+a.x, y0+bottom)
	}
	pdf.SetDashPattern([]float64{}, 0)

	for _, a := range sd.actors {
		pdf.SetDrawColor(80, 110, 170)
		pdf.SetFillColor(232, 240, 254)
		pdf.SetLineWidth(0.3)
		pdf.RoundRect(x0+a.x-a.w/2, y0, a.w, hdr, 1.5, "DF")
		pdf.SetFont("Helvetica", "B", 9)
		pdf.SetTextColor(30, 30, 30)
		th := float64(len(a.lines)) * 4.2
		for k, l := range a.lines {
			pdf.SetXY(x0+a.x-a.w/2, y0+(hdr-th)/2+float64(k)*4.2)
			pdf.Cell(a.w, 4.2, l, "C", false)
		}
	}

	for _, it := range sd.items {
		if it.chunk != c {
			continue
		}
		switch it.kind {
		case "msg":
			r.seqMsg(sd, it, x0, y0)
		case "note":
			left, w := sd.noteBox(it)
			pdf.SetDrawColor(200, 180, 80)
			pdf.SetFillColor(255, 250, 205)
			pdf.SetLineWidth(0.3)
			pdf.Rect(x0+left, y0+it.y+1, w, it.h-3, "DF")
			pdf.SetFont("Helvetica", "", seqFont)
			pdf.SetTextColor(30, 30, 30)
			r.seqText(x0+left, y0+it.y+3, w, it.lines, "C")
		}
	}
}

func (r *renderer) seqMsg(sd *sequence, it *seqItem, x0, y0 float64) {
	pdf := r.pdf
	xf, xt := x0+sd.actors[it.from].x, x0+sd.actors[it.to].x
	top := y0 + it.y
	pdf.SetFont("Helvetica", "", seqFont)
	pdf.SetTextColor(30, 30, 30)
	pdf.SetDrawColor(60, 60, 60)
	pdf.SetFillColor(60, 60, 60)
	pdf.SetLineWidth(0.3)

	if it.from == it.to {
		y1 := top + 1.5
		y2 := math.Max(top+it.h-2.5, y1+4)
		r.seqText(xf+10, top+1, it.tw+2, it.lines, "L")
		if it.dashed {
			pdf.SetDashPattern([]float64{1.5, 1.2}, 0)
		}
		pdf.Line(xf, y1, xf+8, y1)
		pdf.Line(xf+8, y1, xf+8, y2)
		end := xf
		if it.head == "filled" || it.head == "async" {
			end = xf + seqHead
		}
		pdf.Line(xf+8, y2, end, y2)
		pdf.SetDashPattern([]float64{}, 0)
		r.seqArrowHead(it.head, xf, y2, -1, 0)
		return
	}

	ay := top + it.h - 2.5
	left := math.Min(xf, xt)
	r.seqText(left, top+1, math.Abs(xt-xf), it.lines, "C")
	d := 1.0
	if xt < xf {
		d = -1
	}
	end := xt
	if it.head == "filled" {
		end = xt - d*seqHead
	}
	if it.dashed {
		pdf.SetDashPattern([]float64{1.5, 1.2}, 0)
	}
	pdf.Line(xf, ay, end, ay)
	pdf.SetDashPattern([]float64{}, 0)
	r.seqArrowHead(it.head, xt, ay, d, 0)
}

func (r *renderer) seqArrowHead(kind string, tx, ty, ux, uy float64) {
	pdf := r.pdf
	bx, by := tx-ux*seqHead, ty-uy*seqHead
	px, py := -uy*1.1, ux*1.1
	switch kind {
	case "filled":
		pdf.Polygon([]minipdf.Point{{X: tx, Y: ty}, {X: bx + px, Y: by + py}, {X: bx - px, Y: by - py}}, "F")
	case "async":
		pdf.Line(tx, ty, bx+px, by+py)
		pdf.Line(tx, ty, bx-px, by-py)
	case "cross":
		cx, cy := tx-ux*1.5, ty-uy*1.5
		pdf.Line(cx-1.2, cy-1.2, cx+1.2, cy+1.2)
		pdf.Line(cx-1.2, cy+1.2, cx+1.2, cy-1.2)
	}
}
