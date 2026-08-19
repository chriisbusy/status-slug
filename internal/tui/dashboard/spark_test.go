package dashboard

import (
	"strings"
	"testing"
)

func TestSparkEmpty(t *testing.T) {
	got := Spark(nil, 5, "braille")
	if len([]rune(got)) != 5 {
		t.Errorf("empty spark: got %d cells want 5", len([]rune(got)))
	}
	// All cells should be blank braille (U+2800).
	for _, r := range got {
		if r != '\u2800' {
			t.Errorf("expected blank braille, got %U", r)
		}
	}
}

func TestSparkWidth(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	for _, w := range []int{1, 4, 8, 16} {
		got := Spark(vals, w, "braille")
		if len([]rune(got)) != w {
			t.Errorf("width %d: got %d cells", w, len([]rune(got)))
		}
	}
}

func TestSparkFlatLine(t *testing.T) {
	// All same values → mid-height flat line (level 2 → rows 3,2 filled).
	vals := []float64{5, 5, 5, 5}
	got := Spark(vals, 2, "braille")
	// Every cell should have some dots set (not blank).
	for _, r := range got {
		if r == '\u2800' {
			t.Error("flat series should not render blank cells")
		}
	}
}

func TestSparkIncreasing(t *testing.T) {
	// Increasing series: left cells lower (fewer dots), right cells higher.
	vals := []float64{0, 0, 0, 0, 10, 10, 10, 10}
	got := Spark(vals, 4, "braille")
	runes := []rune(got)
	// Right cells should have more dots than left cells.
	leftDots := popcount(int(runes[0]) - 0x2800)
	rightDots := popcount(int(runes[3]) - 0x2800)
	if rightDots <= leftDots {
		t.Errorf("increasing series: right cell %08b should have more dots than left %08b",
			int(runes[3])-0x2800, int(runes[0])-0x2800)
	}
}

func TestSparkAllBrailleRange(t *testing.T) {
	vals := []float64{1, 100, 50, 75}
	got := Spark(vals, 4, "braille")
	for _, r := range got {
		if r < 0x2800 || r > 0x28FF {
			t.Errorf("rune %U outside braille block", r)
		}
	}
}

func TestSparkNoSpaces(t *testing.T) {
	// A populated spark should contain no spaces.
	vals := []float64{10, 20, 30, 40, 50}
	got := Spark(vals, 5, "braille")
	if strings.Contains(got, " ") {
		t.Error("spark contains spaces")
	}
}

func popcount(n int) int {
	count := 0
	for n > 0 {
		count += n & 1
		n >>= 1
	}
	return count
}

// TestSparkGlyphSets: blocks and ascii ramps render distinct levels and
// unknown sets fall back to braille (graph_glyphs setting).
func TestSparkGlyphSets(t *testing.T) {
	vals := []float64{0, 0, 0, 0, 10, 10, 10, 10}
	for _, set := range []string{"blocks", "ascii", "braille", "bogus"} {
		got := Spark(vals, 8, set)
		if len([]rune(got)) != 8 {
			t.Errorf("set %q: got %d cells want 8", set, len([]rune(got)))
		}
	}
	blocks := []rune(Spark(vals, 8, "blocks"))
	// Rising series: last cell should be a higher block than the first.
	if blocks[7] <= blocks[0] {
		t.Errorf("blocks rising: first %U last %U", blocks[0], blocks[7])
	}
	// Full-range assertion: ascii and blocks sets must contain ZERO runes
	// from the braille block (U+2800–U+28FF), not just two sampled glyphs.
	for _, set := range []string{"ascii", "blocks"} {
		for _, r := range Spark(vals, 8, set) {
			if r >= 0x2800 && r <= 0x28FF {
				t.Errorf("set %q contains braille rune %U", set, r)
			}
		}
	}
	if !strings.Contains(Spark(vals, 8, "bogus"), "⣿") && !strings.ContainsRune(Spark(vals, 8, "bogus"), 0x2800) {
		// bogus falls back to braille: braille-range runes expected
		for _, r := range Spark(vals, 8, "bogus") {
			if r < 0x2800 || r > 0x28FF {
				t.Errorf("bogus set should fall back to braille, got %U", r)
			}
		}
	}
}
