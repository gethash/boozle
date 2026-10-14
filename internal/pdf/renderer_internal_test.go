package pdf

import "testing"

func TestPageSizeIsServedFromPreloadedSizes(t *testing.T) {
	doc := &Doc{pageSizes: []Page{{WidthPoints: 1024, HeightPoints: 768}, {WidthPoints: 10, HeightPoints: 20}}}
	got, err := doc.PageSize(0)
	if err != nil {
		t.Fatalf("PageSize(0): %v", err)
	}
	if got.WidthPoints != 1024 || got.HeightPoints != 768 {
		t.Fatalf("PageSize(0) = %+v, want 1024x768", got)
	}
	if _, err := doc.PageSize(2); err == nil {
		t.Fatal("PageSize(2) on a 2-page doc should fail")
	}
	if doc.PageCount() != 2 {
		t.Fatalf("PageCount = %d, want 2", doc.PageCount())
	}
}
