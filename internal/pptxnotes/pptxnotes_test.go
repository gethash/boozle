package pptxnotes

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractSpeakerNotes(t *testing.T) {
	pptx := filepath.Join(t.TempDir(), "deck.pptx")
	writeTestPPTX(t, pptx)

	notes, err := Extract(pptx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("len(notes) = %d, want 1: %+v", len(notes), notes)
	}
	if notes[0].N != 1 {
		t.Fatalf("slide number = %d, want 1", notes[0].N)
	}
	if notes[0].Notes != "First point\nSecond point" {
		t.Fatalf("notes = %q", notes[0].Notes)
	}
}

func TestExtractIgnoresSlideNumberOnlyNotes(t *testing.T) {
	pptx := filepath.Join(t.TempDir(), "deck.pptx")
	writeSlideNumberOnlyPPTX(t, pptx)

	notes, err := Extract(pptx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("len(notes) = %d, want 0: %+v", len(notes), notes)
	}
}

func TestWriteSidecar(t *testing.T) {
	out := filepath.Join(t.TempDir(), "deck.boozle.toml")
	err := WriteSidecar(out, []SlideNote{{N: 2, Notes: "Speaker note"}}, false)
	if err != nil {
		t.Fatalf("WriteSidecar: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`[[page]]`, `n = 2`, `notes = "Speaker note"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("sidecar missing %q:\n%s", want, text)
		}
	}
	if err := WriteSidecar(out, nil, false); err == nil {
		t.Fatal("WriteSidecar should refuse to overwrite without force")
	}
}

func writeTestPPTX(t *testing.T, pptx string) {
	t.Helper()
	f, err := os.Create(pptx)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	add := func(name, body string) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("ppt/presentation.xml", `<?xml version="1.0" encoding="UTF-8"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst><p:sldId id="256" r:id="rId1"/><p:sldId id="257" r:id="rId2"/></p:sldIdLst>
</p:presentation>`)
	add("ppt/_rels/presentation.xml.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/>
</Relationships>`)
	add("ppt/slides/slide1.xml", `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`)
	add("ppt/slides/slide2.xml", `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`)
	add("ppt/slides/_rels/slide1.xml.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdNotes" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>
</Relationships>`)
	add("ppt/notesSlides/notesSlide1.xml", `<?xml version="1.0" encoding="UTF-8"?>
<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree>
    <p:sp><p:nvSpPr><p:nvPr><p:ph type="body"/></p:nvPr></p:nvSpPr><p:txBody>
      <a:p><a:r><a:t>First point</a:t></a:r></a:p>
      <a:p><a:r><a:t>Second point</a:t></a:r></a:p>
    </p:txBody></p:sp>
  </p:spTree></p:cSld>
</p:notes>`)
}

func writeSlideNumberOnlyPPTX(t *testing.T, pptx string) {
	t.Helper()
	f, err := os.Create(pptx)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	add := func(name, body string) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("ppt/presentation.xml", `<?xml version="1.0" encoding="UTF-8"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst>
</p:presentation>`)
	add("ppt/_rels/presentation.xml.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`)
	add("ppt/slides/slide1.xml", `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`)
	add("ppt/slides/_rels/slide1.xml.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdNotes" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>
</Relationships>`)
	add("ppt/notesSlides/notesSlide1.xml", `<?xml version="1.0" encoding="UTF-8"?>
<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree>
    <p:sp><p:nvSpPr><p:nvPr><p:ph type="body"/></p:nvPr></p:nvSpPr><p:txBody>
      <a:p><a:r><a:t>1</a:t></a:r></a:p>
    </p:txBody></p:sp>
  </p:spTree></p:cSld>
</p:notes>`)
}
