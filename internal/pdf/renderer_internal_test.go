package pdf

import "testing"

func TestPageSizeReturnsCachedValueWithoutPDFiumCall(t *testing.T) {
	doc := &Doc{
		pages:     2,
		pageSizes: []Page{{Index: 0, WidthPoints: 1024, HeightPoints: 768}},
		sizeKnown: []bool{true, false},
	}
	got, err := doc.PageSize(0)
	if err != nil {
		t.Fatalf("PageSize cached value: %v", err)
	}
	if got.WidthPoints != 1024 || got.HeightPoints != 768 {
		t.Fatalf("PageSize = %+v, want cached 1024x768", got)
	}
}
