package dashboard

// spark.go — pure braille sparkline renderer.
// Maps a []float64 ring buffer to a string of braille cells (2×4 dots per cell).

// brailleBase is U+2800; dots are set by bit position.
const brailleBase = 0x2800

// dotBit maps (col, row) within a 2×4 cell to the braille dot bit.
var dotBit = [2][4]int{
	{0, 1, 2, 6}, // left column: dots 1,2,3,7
	{3, 4, 5, 7}, // right column: dots 4,5,6,8
}

// Spark renders values as a sparkline of exactly width cells using the
// configured glyph set: "braille" (2×4 dots), "blocks" (▁▂▃▄▅▆▇█), or
// "ascii" (" .:-=+*#%@"). Unknown sets fall back to braille.
func Spark(values []float64, width int, set string) string {
	switch set {
	case "blocks":
		return sparkRamp(values, width, []rune(" ▁▂▃▄▅▆▇█"))
	case "ascii":
		return sparkRamp(values, width, []rune(" .:-=+*#%@"))
	default:
		return sparkBraille(values, width)
	}
}

// sparkRamp renders one glyph per column using a leveled ramp.
func sparkRamp(values []float64, width int, ramp []rune) string {
	if width <= 0 {
		return ""
	}
	out := make([]rune, width)
	if len(values) == 0 {
		for i := range out {
			out[i] = ramp[0]
		}
		return string(out)
	}
	lo, hi := values[0], values[0]
	for _, v := range values {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	rng := hi - lo
	sampled := resample(values, width)
	levels := len(ramp) - 1
	for i, v := range sampled {
		var lvl int
		if rng == 0 {
			lvl = levels / 2
		} else {
			lvl = int((v - lo) / rng * float64(levels))
			if lvl > levels {
				lvl = levels
			}
		}
		out[i] = ramp[lvl]
	}
	return string(out)
}

// sparkBraille is the original 2×4-dot renderer.
func sparkBraille(values []float64, width int) string {
	if width <= 0 {
		return ""
	}
	if len(values) == 0 {
		return string(repeatBraille(width, 0))
	}

	lo, hi := values[0], values[0]
	for _, v := range values {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	rng := hi - lo

	// Resample values to width*2 columns.
	cols := width * 2
	sampled := resample(values, cols)

	cells := make([]int, width)
	for c := 0; c < cols; c++ {
		v := sampled[c]
		// Normalize to 0..3 (4 row levels).
		var level int
		if rng == 0 {
			level = 2 // flat: mid-height
		} else {
			level = int((v - lo) / rng * 3.999)
		}
		cellIdx := c / 2
		colIdx := c % 2
		// Fill dots from bottom (row 3) up to the level.
		for row := 3; row >= 3-level; row-- {
			cells[cellIdx] |= 1 << dotBit[colIdx][row]
		}
	}

	runes := make([]rune, width)
	for i, bits := range cells {
		runes[i] = rune(brailleBase + bits)
	}
	return string(runes)
}

// resample stretches or compresses values to exactly n columns by
// nearest-neighbor sampling.
func resample(values []float64, n int) []float64 {
	if n <= 0 || len(values) == 0 {
		return nil
	}
	out := make([]float64, n)
	for i := range out {
		idx := int(float64(i) / float64(n) * float64(len(values)))
		if idx >= len(values) {
			idx = len(values) - 1
		}
		out[i] = values[idx]
	}
	return out
}

// repeatBraille returns n blank braille characters.
func repeatBraille(n, bits int) []rune {
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = rune(brailleBase + bits)
	}
	return runes
}
