// Package pptxnotes extracts speaker notes from PowerPoint .pptx files.
package pptxnotes

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// SlideNote is the extracted speaker note text for a 1-indexed slide.
type SlideNote struct {
	N     int    `toml:"n"`
	Notes string `toml:"notes"`
}

// Extract reads path as a .pptx file and returns speaker notes in slide order.
// Slides without notes are omitted.
func Extract(pptxPath string) ([]SlideNote, error) {
	zr, err := zip.OpenReader(pptxPath)
	if err != nil {
		return nil, fmt.Errorf("open pptx %s: %w", pptxPath, err)
	}
	defer zr.Close()

	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}

	presentationRels, err := readRelationships(files, "ppt/_rels/presentation.xml.rels")
	if err != nil {
		return nil, err
	}
	slideRelIDs, err := readSlideRelIDs(files, "ppt/presentation.xml")
	if err != nil {
		return nil, err
	}

	out := make([]SlideNote, 0, len(slideRelIDs))
	for i, relID := range slideRelIDs {
		slideTarget, ok := presentationRels[relID]
		if !ok {
			continue
		}
		slidePath := resolveTarget("ppt", slideTarget)
		slideRelsPath := relsPath(slidePath)
		slideRels, err := readRelationships(files, slideRelsPath)
		if err != nil {
			if isMissingZipFile(err) {
				continue
			}
			return nil, err
		}
		notesPath := ""
		for _, target := range slideRels {
			if strings.Contains(target, "notesSlides/") {
				notesPath = resolveTarget(path.Dir(slidePath), target)
				break
			}
		}
		if notesPath == "" {
			continue
		}
		notesXML, err := readZipFile(files, notesPath)
		if err != nil {
			if isMissingZipFile(err) {
				continue
			}
			return nil, err
		}
		notes, err := extractNotesText(notesXML, i+1)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", notesPath, err)
		}
		if notes != "" {
			out = append(out, SlideNote{N: i + 1, Notes: notes})
		}
	}
	return out, nil
}

// WriteSidecar writes a standalone Boozle TOML sidecar containing notes.
func WriteSidecar(outPath string, notes []SlideNote, force bool) error {
	if !force {
		if _, err := os.Stat(outPath); err == nil {
			return fmt.Errorf("%s already exists (pass --force to overwrite)", outPath)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].N < notes[j].N })

	var buf bytes.Buffer
	buf.WriteString("# boozle sidecar configuration generated from PowerPoint speaker notes\n\n")
	if err := toml.NewEncoder(&buf).Encode(struct {
		Page []SlideNote `toml:"page"`
	}{Page: notes}); err != nil {
		return err
	}
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}

// DefaultSidecarPath returns the conventional sidecar path for a source file.
func DefaultSidecarPath(src string) string {
	ext := filepath.Ext(src)
	return strings.TrimSuffix(src, ext) + ".boozle.toml"
}

func readSlideRelIDs(files map[string]*zip.File, name string) ([]string, error) {
	data, err := readZipFile(files, name)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var ids []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return ids, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: parse XML: %w", name, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "sldId" {
			continue
		}
		for _, a := range se.Attr {
			if a.Name.Local == "id" && strings.Contains(a.Name.Space, "relationships") {
				ids = append(ids, a.Value)
				break
			}
		}
	}
}

func readRelationships(files map[string]*zip.File, name string) (map[string]string, error) {
	data, err := readZipFile(files, name)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	rels := map[string]string{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return rels, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: parse XML: %w", name, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}
		var id, target string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "Id":
				id = a.Value
			case "Target":
				target = a.Value
			}
		}
		if id != "" && target != "" {
			rels[id] = target
		}
	}
}

func extractNotesText(data []byte, slideNum int) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var inShape, bodyPlaceholder bool
	var curPara []string
	var bodyParas, fallbackParas []string
	flush := func() {
		text := strings.TrimSpace(strings.Join(curPara, ""))
		curPara = nil
		if text == "" {
			return
		}
		if bodyPlaceholder {
			bodyParas = append(bodyParas, text)
		}
		fallbackParas = append(fallbackParas, text)
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			flush()
			if len(bodyParas) > 0 {
				return joinUsefulNotes(bodyParas, slideNum), nil
			}
			return joinUsefulNotes(fallbackParas, slideNum), nil
		}
		if err != nil {
			return "", fmt.Errorf("parse XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sp":
				inShape = true
				bodyPlaceholder = false
			case "ph":
				if inShape && placeholderType(t) == "body" {
					bodyPlaceholder = true
				}
			case "p":
				if inShape {
					flush()
				}
			case "t":
				if inShape {
					var s string
					if err := dec.DecodeElement(&s, &t); err != nil {
						return "", err
					}
					curPara = append(curPara, s)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p":
				if inShape {
					flush()
				}
			case "sp":
				flush()
				inShape = false
				bodyPlaceholder = false
			}
		}
	}
}

func joinUsefulNotes(paras []string, slideNum int) string {
	filtered := make([]string, 0, len(paras))
	slideNumText := fmt.Sprint(slideNum)
	for _, para := range paras {
		para = strings.TrimSpace(para)
		if para == "" || para == slideNumText {
			continue
		}
		filtered = append(filtered, para)
	}
	return strings.Join(filtered, "\n")
}

func placeholderType(se xml.StartElement) string {
	for _, a := range se.Attr {
		if a.Name.Local == "type" {
			return a.Value
		}
	}
	return ""
}

func readZipFile(files map[string]*zip.File, name string) ([]byte, error) {
	f, ok := files[name]
	if !ok {
		return nil, missingZipFile(name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

type missingZipFile string

func (m missingZipFile) Error() string { return fmt.Sprintf("pptx file missing %s", string(m)) }

func isMissingZipFile(err error) bool {
	_, ok := err.(missingZipFile)
	return ok
}

func resolveTarget(baseDir, target string) string {
	target = strings.TrimPrefix(target, "/")
	return path.Clean(path.Join(baseDir, target))
}

func relsPath(part string) string {
	dir, file := path.Split(part)
	return path.Join(dir, "_rels", file+".rels")
}
