package main

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"markdown-converter/minipdf"
)

const (
	fcFont    = 9.0
	fcLabelSz = 8.0
	fcLine    = 4.2
	fcMaxText = 34.0
	fcArrow   = 2.4

	fcLabelMax = 24.0
	fcLabelLn  = 3.6
)

type fnode struct {
	id, label, shape string
	lines            []string
	w, h, x, y       float64 // x, y are the center
	layer            int
}

type fedge struct {
	from, to int
	label    string
	arrow    bool
	line     string // solid, dotted, thick
	back     bool
	lines    []string
	lw       float64
}

type flowchart struct {
	horizontal bool
	dir        string
	nodes      []*fnode
	idx        map[string]int
	edges      []*fedge
}

var (
	flowHeader = regexp.MustCompile(`^(?:flowchart|graph)(?:\s+(LR|RL|TD|TB|BT))?$`)
	nodeID     = regexp.MustCompile(`^[A-Za-z0-9_]+`)
	brTag      = regexp.MustCompile(`(?i)<br\s*/?>`)

	shapes = []struct{ open, close, shape string }{
		{"((", "))", "circle"}, {"([", "])", "stadium"}, {"{{", "}}", "hexagon"}, {"[[", "]]", "sub"},
		{"[", "]", "rect"}, {"(", ")", "round"}, {"{", "}", "diamond"},
	}

	edgeKinds = []struct {
		re    *regexp.Regexp
		line  string
		arrow bool
	}{
		{regexp.MustCompile(`^--\s+(.+?)\s+-->`), "solid", true},
		{regexp.MustCompile(`^--\s+(.+?)\s+---`), "solid", false},
		{regexp.MustCompile(`^==\s+(.+?)\s+==>`), "thick", true},
		{regexp.MustCompile(`^-\.\s+(.+?)\s+\.->`), "dotted", true},
		{regexp.MustCompile(`^-{2,}>`), "solid", true},
		{regexp.MustCompile(`^-{3,}`), "solid", false},
		{regexp.MustCompile(`^={2,}>`), "thick", true},
		{regexp.MustCompile(`^={3,}`), "thick", false},
		{regexp.MustCompile(`^-\.+->`), "dotted", true},
		{regexp.MustCompile(`^-\.+-`), "dotted", false},
	}
)

// parseFlowchart returns false when the code is not a flowchart or uses unsupported syntax.
func parseFlowchart(code string) (*flowchart, bool) {
	fc := &flowchart{idx: map[string]int{}}
	header := false
	for _, ln := range strings.Split(code, "\n") {
		ln = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ln), ";"))
		if ln == "" || strings.HasPrefix(ln, "%%") {
			continue
		}
		if !header {
			m := flowHeader.FindStringSubmatch(ln)
			if m == nil {
				return nil, false
			}
			fc.dir = m[1]
			if fc.dir == "" {
				fc.dir = "TD"
			}
			header = true
			continue
		}
		switch strings.Fields(ln)[0] {
		case "subgraph", "end", "style", "classDef", "class", "linkStyle", "click", "direction":
			continue
		}
		if !fc.parseStatement(ln) {
			return nil, false
		}
	}
	return fc, header && len(fc.nodes) > 0
}

func (fc *flowchart) parseStatement(s string) bool {
	prev := -1
	var pending *fedge
	for {
		s = strings.TrimSpace(s)
		if s == "" {
			return pending == nil
		}
		if prev >= 0 && pending == nil {
			e, rest, ok := parseEdge(s)
			if !ok {
				return false
			}
			pending, s = e, rest
			continue
		}
		i, rest, ok := fc.parseNode(s)
		if !ok {
			return false
		}
		if pending != nil {
			pending.from, pending.to = prev, i
			fc.edges = append(fc.edges, pending)
			pending = nil
		}
		prev, s = i, rest
	}
}

func parseEdge(s string) (*fedge, string, bool) {
	for _, k := range edgeKinds {
		m := k.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		e := &fedge{line: k.line, arrow: k.arrow}
		rest := s[len(m[0]):]
		if len(m) > 1 {
			e.label = m[1]
		}
		if strings.HasPrefix(rest, "|") {
			end := strings.Index(rest[1:], "|")
			if end < 0 {
				return nil, "", false
			}
			e.label = strings.Trim(strings.TrimSpace(rest[1:1+end]), `"`)
			rest = rest[end+2:]
		}
		return e, rest, true
	}
	return nil, "", false
}

func (fc *flowchart) parseNode(s string) (int, string, bool) {
	id := nodeID.FindString(s)
	if id == "" {
		return 0, "", false
	}
	rest := s[len(id):]
	label, shape := "", ""
	for _, sh := range shapes {
		if !strings.HasPrefix(rest, sh.open) {
			continue
		}
		body := rest[len(sh.open):]
		end := strings.Index(body, sh.close)
		if end < 0 {
			return 0, "", false
		}
		label = strings.Trim(strings.TrimSpace(body[:end]), `"`)
		shape = sh.shape
		rest = body[end+len(sh.close):]
		break
	}
	i, ok := fc.idx[id]
	if !ok {
		i = len(fc.nodes)
		fc.idx[id] = i
		fc.nodes = append(fc.nodes, &fnode{id: id, label: id, shape: "rect"})
	}
	if shape != "" {
		fc.nodes[i].label, fc.nodes[i].shape = label, shape
	}
	return i, rest, true
}

// measure computes node sizes and edge label widths using current fonts.
func (fc *flowchart) measure(r *renderer) {
	pdf := r.pdf
	for _, n := range fc.nodes {
		pdf.SetFont("Helvetica", "", fcFont)
		text := r.tr(brTag.ReplaceAllString(n.label, "\n"))
		maxW := fcMaxText
		for _, word := range strings.Fields(text) {
			maxW = math.Max(maxW, pdf.GetStringWidth(word))
		}
		n.lines = nil
		tw := 0.0
		for _, part := range strings.Split(text, "\n") {
			n.lines = append(n.lines, r.wrap(part, maxW)...)
		}
		for _, l := range n.lines {
			tw = math.Max(tw, pdf.GetStringWidth(l))
		}
		th := float64(len(n.lines)) * fcLine
		switch n.shape {
		case "diamond":
			n.w, n.h = 2*tw+8, 2*th+6
		case "circle":
			n.w, n.h = 1.4*tw+8, 1.4*th+8
		case "hexagon":
			n.w, n.h = tw+th+8, th+6
		case "stadium":
			n.w, n.h = tw+th+6, th+5
		default:
			n.w, n.h = tw+8, th+5
		}
	}
	pdf.SetFont("Helvetica", "", fcLabelSz)
	for _, e := range fc.edges {
		e.lines, e.lw = nil, 0
		if e.label == "" {
			continue
		}
		e.lines = r.wrap(r.tr(brTag.ReplaceAllString(e.label, " ")), fcLabelMax)
		for _, l := range e.lines {
			e.lw = math.Max(e.lw, pdf.GetStringWidth(l))
		}
	}
}

// assignLayers returns node indices grouped by layer, ordered to reduce crossings.
func (fc *flowchart) assignLayers() [][]int {
	n := len(fc.nodes)
	out := make([][]*fedge, n)
	for _, e := range fc.edges {
		out[e.from] = append(out[e.from], e)
	}
	state := make([]int, n)
	var dfs func(int)
	dfs = func(u int) {
		state[u] = 1
		for _, e := range out[u] {
			switch state[e.to] {
			case 1:
				e.back = true
			case 0:
				dfs(e.to)
			}
		}
		state[u] = 2
	}
	for i := range fc.nodes {
		if state[i] == 0 {
			dfs(i)
		}
	}

	for _, nd := range fc.nodes {
		nd.layer = 0
	}
	for pass := 0; pass < n; pass++ {
		changed := false
		for _, e := range fc.edges {
			if e.back {
				continue
			}
			if l := fc.nodes[e.from].layer + 1; fc.nodes[e.to].layer < l {
				fc.nodes[e.to].layer = l
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	maxL := 0
	for _, nd := range fc.nodes {
		maxL = max(maxL, nd.layer)
	}
	layers := make([][]int, maxL+1)
	for i, nd := range fc.nodes {
		layers[nd.layer] = append(layers[nd.layer], i)
	}

	preds := make([][]int, n)
	succs := make([][]int, n)
	for _, e := range fc.edges {
		if e.back {
			continue
		}
		preds[e.to] = append(preds[e.to], e.from)
		succs[e.from] = append(succs[e.from], e.to)
	}
	pos := make([]float64, n)
	refresh := func() {
		for _, l := range layers {
			for p, i := range l {
				pos[i] = float64(p)
			}
		}
	}
	sweep := func(l []int, nb [][]int) {
		key := map[int]float64{}
		for _, i := range l {
			key[i] = pos[i]
			if len(nb[i]) > 0 {
				s := 0.0
				for _, j := range nb[i] {
					s += pos[j]
				}
				key[i] = s / float64(len(nb[i]))
			}
		}
		sort.SliceStable(l, func(a, b int) bool { return key[l[a]] < key[l[b]] })
		refresh()
	}
	refresh()
	for range 2 {
		for l := 1; l < len(layers); l++ {
			sweep(layers[l], preds)
		}
		for l := len(layers) - 2; l >= 0; l-- {
			sweep(layers[l], succs)
		}
	}
	return layers
}

// place sets node centers and returns the diagram size.
func (fc *flowchart) place(layers [][]int, horizontal bool) (float64, float64) {
	prim := func(n *fnode) float64 {
		if horizontal {
			return n.w
		}
		return n.h
	}
	cross := func(n *fnode) float64 {
		if horizontal {
			return n.h
		}
		return n.w
	}
	crossGap, primGap := 10.0, 12.0
	maxLabel := 0.0
	for _, e := range fc.edges {
		maxLabel = math.Max(maxLabel, e.lw)
	}
	if horizontal {
		crossGap, primGap = 8.0, 12.0+math.Min(maxLabel, 45)
	} else if maxLabel > 0 {
		primGap = 20
	}

	layerPrim := make([]float64, len(layers))
	layerCross := make([]float64, len(layers))
	total := 0.0
	for l, ids := range layers {
		for k, i := range ids {
			n := fc.nodes[i]
			layerPrim[l] = math.Max(layerPrim[l], prim(n))
			layerCross[l] += cross(n)
			if k > 0 {
				layerCross[l] += crossGap
			}
		}
		total = math.Max(total, layerCross[l])
	}
	p := 0.0
	for l, ids := range layers {
		c := (total - layerCross[l]) / 2
		for _, i := range ids {
			n := fc.nodes[i]
			pc, cc := p+layerPrim[l]/2, c+cross(n)/2
			if horizontal {
				n.x, n.y = pc, cc
			} else {
				n.x, n.y = cc, pc
			}
			c += cross(n) + crossGap
		}
		p += layerPrim[l] + primGap
	}
	p -= primGap
	if horizontal {
		return p, total
	}
	return total, p
}

// border returns the distance from the node center to its outline along unit vector (ux, uy).
func (n *fnode) border(ux, uy float64) float64 {
	hw, hh := n.w/2, n.h/2
	switch n.shape {
	case "diamond":
		return 1 / (math.Abs(ux)/hw + math.Abs(uy)/hh)
	case "circle":
		return 1 / math.Hypot(ux/hw, uy/hh)
	}
	return math.Min(hw/math.Abs(ux), hh/math.Abs(uy))
}

func (r *renderer) flowchart(fc *flowchart) {
	pdf := r.pdf
	fc.measure(r)
	layers := fc.assignLayers()

	lm, _, rm, _ := pdf.GetMargins()
	pageW, pageH := pdf.GetPageSize()
	availW, availH := pageW-lm-rm, pageH-2*margin
	fit := func(horizontal bool) float64 {
		w, h := fc.place(layers, horizontal)
		return math.Min(1, math.Min(availW/w, availH/h))
	}

	// Wide left-to-right charts get unreadable on a portrait page; prefer the other axis then.
	horizontal := fc.dir == "LR" || fc.dir == "RL"
	scale := fit(horizontal)
	if scale < 0.7 {
		if alt := fit(!horizontal); alt > scale {
			horizontal, scale = !horizontal, alt
		}
	}
	w, h := fc.place(layers, horizontal)
	fc.horizontal = horizontal

	pdf.Ln(r.lh() * 0.5)
	if pdf.GetY()+h*scale > pageH-margin {
		pdf.AddPage()
	}
	x0, y0 := lm+(availW-w*scale)/2, pdf.GetY()

	pdf.SetAutoPageBreak(false, 0)
	pdf.BeginScale(scale, x0, y0)
	pdf.SetLineWidth(0.3)
	for _, e := range fc.edges {
		r.drawEdge(fc, e, x0, y0)
	}
	for _, n := range fc.nodes {
		r.drawNode(n, x0, y0)
	}
	for _, e := range fc.edges {
		r.drawEdgeLabel(fc, e, x0, y0)
	}
	pdf.EndScale()
	pdf.SetAutoPageBreak(true, margin)
	pdf.SetLineWidth(0.2)

	pdf.SetXY(lm, y0+h*scale)
	pdf.Ln(r.lh() * 1.5)
}

func (r *renderer) drawNode(n *fnode, x0, y0 float64) {
	pdf := r.pdf
	cx, cy := x0+n.x, y0+n.y
	left, top := cx-n.w/2, cy-n.h/2

	pdf.SetDrawColor(80, 110, 170)
	pdf.SetFillColor(232, 240, 254)
	if n.shape == "diamond" {
		pdf.SetDrawColor(190, 150, 60)
		pdf.SetFillColor(255, 246, 220)
	}
	switch n.shape {
	case "round":
		pdf.RoundRect(left, top, n.w, n.h, 2, "DF")
	case "stadium":
		pdf.RoundRect(left, top, n.w, n.h, n.h/2, "DF")
	case "circle":
		pdf.Ellipse(cx, cy, n.w/2, n.h/2, "DF")
	case "diamond":
		pdf.Polygon([]minipdf.Point{{X: cx, Y: top}, {X: left + n.w, Y: cy}, {X: cx, Y: top + n.h}, {X: left, Y: cy}}, "DF")
	case "hexagon":
		k := n.h / 2
		pdf.Polygon([]minipdf.Point{
			{X: left + k, Y: top}, {X: left + n.w - k, Y: top}, {X: left + n.w, Y: cy},
			{X: left + n.w - k, Y: top + n.h}, {X: left + k, Y: top + n.h}, {X: left, Y: cy},
		}, "DF")
	case "sub":
		pdf.Rect(left, top, n.w, n.h, "DF")
		pdf.Line(left+2, top, left+2, top+n.h)
		pdf.Line(left+n.w-2, top, left+n.w-2, top+n.h)
	default:
		pdf.Rect(left, top, n.w, n.h, "DF")
	}

	pdf.SetFont("Helvetica", "", fcFont)
	pdf.SetTextColor(30, 30, 30)
	th := float64(len(n.lines)) * fcLine
	for k, ln := range n.lines {
		pdf.SetXY(left, cy-th/2+float64(k)*fcLine)
		pdf.Cell(n.w, fcLine, ln, "C", false)
	}
}

// edgeEnds returns the clipped start and end points of an edge, or false if it has no length.
func (fc *flowchart) edgeEnds(e *fedge, x0, y0 float64) (ax, ay, bx, by, ux, uy float64, ok bool) {
	a, b := fc.nodes[e.from], fc.nodes[e.to]
	dx, dy := b.x-a.x, b.y-a.y
	d := math.Hypot(dx, dy)
	if e.from == e.to || d < 1e-6 {
		return
	}
	ux, uy = dx/d, dy/d
	da, db := a.border(ux, uy), b.border(ux, uy)
	if da+db >= d {
		return
	}
	return x0 + a.x + ux*da, y0 + a.y + uy*da, x0 + b.x - ux*db, y0 + b.y - uy*db, ux, uy, true
}

func (r *renderer) drawEdge(fc *flowchart, e *fedge, x0, y0 float64) {
	ax, ay, bx, by, ux, uy, ok := fc.edgeEnds(e, x0, y0)
	if !ok {
		return
	}
	pdf := r.pdf
	pdf.SetDrawColor(90, 90, 90)
	pdf.SetFillColor(90, 90, 90)
	pdf.SetLineWidth(0.3)
	switch e.line {
	case "dotted":
		pdf.SetDashPattern([]float64{1.2, 1.2}, 0)
	case "thick":
		pdf.SetLineWidth(0.7)
	}
	ex, ey := bx, by
	if e.arrow {
		ex, ey = bx-ux*fcArrow, by-uy*fcArrow
	}
	pdf.Line(ax, ay, ex, ey)
	pdf.SetDashPattern([]float64{}, 0)
	pdf.SetLineWidth(0.3)
	if e.arrow {
		px, py := -uy*1.1, ux*1.1
		pdf.Polygon([]minipdf.Point{{X: bx, Y: by}, {X: ex + px, Y: ey + py}, {X: ex - px, Y: ey - py}}, "F")
	}
}

func (r *renderer) drawEdgeLabel(fc *flowchart, e *fedge, x0, y0 float64) {
	if len(e.lines) == 0 {
		return
	}
	ax, ay, bx, by, _, _, ok := fc.edgeEnds(e, x0, y0)
	if !ok {
		return
	}
	pdf := r.pdf
	// In vertical layouts sibling edges are closest at the source, so shift labels toward the target.
	t := 0.5
	if !fc.horizontal {
		t = 0.65
	}
	mx, my := ax+(bx-ax)*t, ay+(by-ay)*t
	boxW, boxH := e.lw+2, float64(len(e.lines))*fcLabelLn+1
	pdf.SetFont("Helvetica", "", fcLabelSz)
	pdf.SetFillColor(255, 255, 255)
	pdf.Rect(mx-boxW/2, my-boxH/2, boxW, boxH, "F")
	pdf.SetTextColor(60, 60, 60)
	for k, ln := range e.lines {
		pdf.SetXY(mx-boxW/2, my-boxH/2+0.5+float64(k)*fcLabelLn)
		pdf.Cell(boxW, fcLabelLn, ln, "C", false)
	}
}
