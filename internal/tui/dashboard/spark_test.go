package dashboard

import "testing"

func TestSparkEmptyHasExactWidth(t *testing.T) {
	for _, style := range []string{"tty", "block", "braille"} {
		got := Spark(nil, 5, style)
		if len([]rune(got)) != 5 {
			t.Fatalf("%s empty width = %d, want 5", style, len([]rune(got)))
		}
	}
}

func TestPairGraphDimensions(t *testing.T) {
	values := []float64{20, 80, 40, 100, 60}
	for _, style := range []string{"tty", "block", "braille"} {
		rows := PairGraph(values, 9, 4, 100, style, false)
		if len(rows) != 4 {
			t.Fatalf("%s rows = %d, want 4", style, len(rows))
		}
		for row, line := range rows {
			if len([]rune(line)) != 9 {
				t.Fatalf("%s row %d width = %d, want 9", style, row, len([]rune(line)))
			}
		}
	}
}

func TestGraphStylesProduceDistinctPairCells(t *testing.T) {
	values := []float64{10, 90, 30, 100, 50, 80}
	tty := Spark(values, 8, "tty")
	block := Spark(values, 8, "block")
	braille := Spark(values, 8, "braille")
	if tty == block || tty == braille || block == braille {
		t.Fatalf("styles not distinct: tty=%q block=%q braille=%q", tty, block, braille)
	}
}

func TestPairCellDependsOnPreviousSample(t *testing.T) {
	falling := Spark([]float64{100, 25}, 2, "block")
	flat := Spark([]float64{25, 25}, 2, "block")
	if []rune(falling)[1] == []rune(flat)[1] {
		t.Fatalf("second cell ignored previous sample: falling=%q flat=%q", falling, flat)
	}
}

func TestInvertedPairGraphUsesDownTable(t *testing.T) {
	values := []float64{10, 60, 100, 30}
	up := PairGraph(values, 6, 3, 100, "tty", false)
	down := PairGraph(values, 6, 3, 100, "tty", true)
	if len(up) != len(down) {
		t.Fatalf("row mismatch: up=%d down=%d", len(up), len(down))
	}
	same := true
	for index := range up {
		if up[index] != down[index] {
			same = false
			break
		}
	}
	if same {
		t.Fatalf("inverted graph equals upward graph: %q", up)
	}
}

func TestUnknownGraphStyleFallsBackToTTY(t *testing.T) {
	values := []float64{10, 40, 90}
	if got, want := Spark(values, 5, "bogus"), Spark(values, 5, "tty"); got != want {
		t.Fatalf("unknown style = %q, want tty %q", got, want)
	}
}

func TestShortHistoryIsRightAligned(t *testing.T) {
	got := []rune(Spark([]float64{100}, 4, "block"))
	for index := range 3 {
		if got[index] != ' ' {
			t.Fatalf("short history not right aligned: %q", string(got))
		}
	}
	if got[3] == ' ' {
		t.Fatalf("newest sample missing: %q", string(got))
	}
}

func TestWideHistoryAnchorsNewestSample(t *testing.T) {
	values := make([]float64, 60)
	values[len(values)-1] = 100
	got := []rune(Spark(values, 7, "block"))
	if got[len(got)-1] == ' ' {
		t.Fatalf("newest tail sample omitted: %q", string(got))
	}
}
