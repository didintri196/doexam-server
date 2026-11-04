package docx

import "strings"

// mathConv turns Office Math (OMML) into LaTeX. Fractions, powers, indices, roots,
// brackets and big operators (Σ, ∏, ∫) are converted; anything else keeps its text
// and sets unsupported so the importer can warn.
type mathConv struct {
	unsupported bool
}

var mathSymbols = map[rune]string{
	'×': `\times `, '÷': `\div `, '±': `\pm `, '∓': `\mp `, '·': `\cdot `, '−': `-`,
	'≤': `\le `, '≥': `\ge `, '≠': `\ne `, '≈': `\approx `, '≡': `\equiv `, '∝': `\propto `,
	'∞': `\infty `, '∑': `\sum `, '∏': `\prod `, '∫': `\int `, '∂': `\partial `, '∇': `\nabla `,
	'√': `\surd `, '∈': `\in `, '∉': `\notin `, '⊂': `\subset `, '⊆': `\subseteq `, '∪': `\cup `, '∩': `\cap `,
	'∅': `\emptyset `, '→': `\to `, '←': `\leftarrow `, '⇒': `\Rightarrow `, '⇔': `\Leftrightarrow `,
	'°': `^{\circ}`, '∠': `\angle `, '△': `\triangle `, '⊥': `\perp `, '∥': `\parallel `,
	'α': `\alpha `, 'β': `\beta `, 'γ': `\gamma `, 'δ': `\delta `, 'ε': `\varepsilon `, 'θ': `\theta `,
	'λ': `\lambda `, 'μ': `\mu `, 'π': `\pi `, 'ρ': `\rho `, 'σ': `\sigma `, 'τ': `\tau `, 'φ': `\varphi `,
	'ω': `\omega `, 'Δ': `\Delta `, 'Σ': `\Sigma `, 'Ω': `\Omega `, 'Π': `\Pi `, 'Θ': `\Theta `,
	'{': `\{`, '}': `\}`, '%': `\%`, '#': `\#`, '&': `\&`, '_': `\_`,
}

func texText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if t, ok := mathSymbols[r]; ok {
			b.WriteString(t)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// group wraps x in braces unless it is a single character.
func group(x string) string {
	x = strings.TrimSpace(x)
	if len([]rune(x)) == 1 {
		return x
	}
	return "{" + x + "}"
}

func (m *mathConv) children(n node) string {
	var b strings.Builder
	for _, c := range n.Children {
		b.WriteString(m.node(c))
	}
	return b.String()
}

func (m *mathConv) part(n node, local string) string {
	if c := n.child(nsM, local); c != nil {
		return m.children(*c)
	}
	return ""
}

func (m *mathConv) prop(n node, pr, local string) (string, bool) {
	p := n.child(nsM, pr)
	if p == nil {
		return "", false
	}
	c := p.child(nsM, local)
	if c == nil {
		return "", false
	}
	return c.attr("val"), true
}

func (m *mathConv) node(n node) string {
	if n.XMLName.Space != nsM {
		// w:r inside math (formatting runs) or unknown: keep its text
		if n.XMLName.Local == "t" {
			return texText(n.Text)
		}
		return m.children(n)
	}
	switch n.XMLName.Local {
	case "oMathPara":
		var parts []string
		for _, c := range n.Children {
			if c.XMLName.Local == "oMath" {
				parts = append(parts, m.children(c))
			}
		}
		return strings.Join(parts, `\\ `)
	case "oMath", "e", "num", "den", "sup", "sub", "deg", "fName", "lim":
		return m.children(n)
	case "r":
		return m.children(n)
	case "t":
		return texText(n.Text)
	case "f":
		if typ, _ := m.prop(n, "fPr", "type"); typ == "lin" || typ == "skw" {
			return group(m.part(n, "num")) + "/" + group(m.part(n, "den"))
		}
		return `\frac{` + m.part(n, "num") + `}{` + m.part(n, "den") + `}`
	case "sSup":
		return group(m.part(n, "e")) + "^" + group(m.part(n, "sup"))
	case "sSub":
		return group(m.part(n, "e")) + "_" + group(m.part(n, "sub"))
	case "sSubSup":
		return group(m.part(n, "e")) + "_" + group(m.part(n, "sub")) + "^" + group(m.part(n, "sup"))
	case "sPre":
		return "{}_" + group(m.part(n, "sub")) + "^" + group(m.part(n, "sup")) + group(m.part(n, "e"))
	case "rad":
		deg := strings.TrimSpace(m.part(n, "deg"))
		if hide, _ := m.prop(n, "radPr", "degHide"); hide == "1" || hide == "on" || hide == "true" || deg == "" {
			return `\sqrt{` + m.part(n, "e") + `}`
		}
		return `\sqrt[` + deg + `]{` + m.part(n, "e") + `}`
	case "d":
		beg, endc, sep := "(", ")", ","
		if v, ok := m.prop(n, "dPr", "begChr"); ok {
			beg = v
		}
		if v, ok := m.prop(n, "dPr", "endChr"); ok {
			endc = v
		}
		if v, ok := m.prop(n, "dPr", "sepChr"); ok {
			sep = v
		}
		var parts []string
		for _, c := range n.Children {
			if c.XMLName.Local == "e" {
				parts = append(parts, m.children(c))
			}
		}
		return `\left` + delim(beg) + strings.Join(parts, texText(sep)) + `\right` + delim(endc)
	case "nary":
		chr := "∫"
		if v, ok := m.prop(n, "naryPr", "chr"); ok && v != "" {
			chr = v
		}
		op := strings.TrimSpace(texText(chr))
		out := op
		if sub := strings.TrimSpace(m.part(n, "sub")); sub != "" {
			out += "_" + group(sub)
		}
		if sup := strings.TrimSpace(m.part(n, "sup")); sup != "" {
			out += "^" + group(sup)
		}
		return out + " " + m.part(n, "e")
	case "func":
		name := strings.TrimSpace(m.part(n, "fName"))
		if known := map[string]bool{"sin": true, "cos": true, "tan": true, "log": true, "ln": true, "lim": true, "exp": true, "cot": true, "sec": true, "csc": true}; known[name] {
			name = `\` + name
		}
		return name + " " + group(m.part(n, "e"))
	case "bar":
		return `\overline{` + m.part(n, "e") + `}`
	case "acc":
		if v, ok := m.prop(n, "accPr", "chr"); ok && v == "⃗" {
			return `\vec{` + m.part(n, "e") + `}`
		}
		return `\hat{` + m.part(n, "e") + `}`
	case "limLow":
		return group(m.part(n, "e")) + "_" + group(m.part(n, "lim"))
	case "limUpp":
		return group(m.part(n, "e")) + "^" + group(m.part(n, "lim"))
	case "rPr", "fPr", "sSupPr", "sSubPr", "sSubSupPr", "radPr", "dPr", "naryPr", "funcPr", "barPr", "accPr",
		"limLowPr", "limUppPr", "ctrlPr", "oMathParaPr", "sPrePr":
		return ""
	}
	// matrices, equation arrays, boxes, group characters…: keep the text, flag it
	m.unsupported = true
	return m.children(n)
}

func delim(c string) string {
	switch c {
	case "":
		return "."
	case "{":
		return `\{`
	case "}":
		return `\}`
	case "|":
		return "|"
	case "‖":
		return `\|`
	case "⟨", "〈":
		return `\langle `
	case "⟩", "〉":
		return `\rangle `
	}
	return c
}
