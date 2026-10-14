// Package pdftest writes tiny, valid PDFs for tests and examples, so the
// PDFium render path can be exercised without checked-in binary fixtures.
package pdftest

import (
	"bytes"
	"fmt"
	"os"
)

// Page describes one generated page: its size in PDF points, the RGB
// colour (0..1) that fills it edge to edge, and optional centred-ish
// Helvetica text lines drawn in white.
type Page struct {
	W, H    float64
	R, G, B float64
	Lines   []string
}

// Bytes returns a PDF document with one page per entry in pages.
func Bytes(pages []Page) []byte {
	var buf bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")

	// Object layout: 1 = catalog, 2 = page tree, then (page, content)
	// pairs, so page i is object 3+2i and its content stream 4+2i.
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := ""
	for i := range pages {
		kids += fmt.Sprintf("%d 0 R ", 3+2*i)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(pages)))
	for i, p := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Contents %d 0 R "+
			"/Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> >>",
			p.W, p.H, 4+2*i))
		content := fmt.Sprintf("%g %g %g rg 0 0 %g %g re f", p.R, p.G, p.B, p.W, p.H)
		size := p.H / 12
		for j, line := range p.Lines {
			y := p.H*0.6 - float64(j)*size*1.5
			content += fmt.Sprintf("\nBT 1 1 1 rg /F1 %g Tf %g %g Td (%s) Tj ET", size, p.W*0.08, y, escape(line))
		}
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes()
}

// escape quotes a string for a PDF literal string.
func escape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '(', ')', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Write writes Bytes(pages) to path.
func Write(path string, pages []Page) error {
	return os.WriteFile(path, Bytes(pages), 0o644)
}
