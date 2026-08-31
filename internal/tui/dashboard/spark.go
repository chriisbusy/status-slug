package dashboard

import "math"

// Spark renders a one-row previous/current pair graph of exactly width cells.
func Spark(values []float64, width int, style string) string {
	maximum := 0.0
	for _, value := range values {
		maximum = max(maximum, value)
	}
	if maximum <= 0 {
		maximum = 1
	}
	rows := PairGraph(values, width, 1, maximum, style, false)
	if len(rows) == 0 {
		return ""
	}
	return rows[0]
}

// PairGraph renders btop's 5x5 previous/current sample cells. Each terminal
// cell encodes one level from the previous sample and one from the current.
func PairGraph(values []float64, width, rows int, maximum float64, style string, invert bool) []string {
	if width <= 0 || rows <= 0 {
		return nil
	}
	if maximum <= 0 {
		maximum = 1
	}
	symbols := pairSymbols(style, invert)
	lines := make([][]rune, rows)
	for y := range lines {
		lines[y] = make([]rune, width)
		for x := range lines[y] {
			lines[y][x] = ' '
		}
	}
	if len(values) == 0 {
		return graphStrings(lines)
	}
	start := 0
	if len(values) < width {
		start = width - len(values)
	}
	for column := range width - start {
		x := start + column
		currentIndex := column
		if len(values) > width {
			if width == 1 {
				currentIndex = len(values) - 1
			} else {
				currentIndex = int(math.Round(float64(x) * float64(len(values)-1) / float64(width-1)))
			}
		}
		previousIndex := max(0, currentIndex-1)
		previous := clampLevel(values[previousIndex], maximum)
		current := clampLevel(values[currentIndex], maximum)
		for horizon := range rows {
			currentHigh := int(math.Round(100 * float64(rows-horizon) / float64(rows)))
			currentLow := int(math.Round(100 * float64(rows-(horizon+1)) / float64(rows)))
			level := func(value int) int {
				if value >= currentHigh {
					return 4
				}
				if value <= currentLow {
					return 0
				}
				return min(4, max(0, int(math.Round(float64(value-currentLow)*4/float64(currentHigh-currentLow)+0.1))))
			}
			lines[horizon][x] = symbols[level(previous)*5+level(current)]
		}
	}
	if invert {
		for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
			lines[i], lines[j] = lines[j], lines[i]
		}
	}
	return graphStrings(lines)
}

func clampLevel(value, maximum float64) int {
	ratio := value / maximum
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return int(math.Round(ratio * 100))
}

func graphStrings(lines [][]rune) []string {
	out := make([]string, len(lines))
	for i := range lines {
		out[i] = string(lines[i])
	}
	return out
}

func pairSymbols(style string, invert bool) []rune {
	var symbols string
	switch style {
	case "braille":
		if invert {
			symbols = " ⠈⠘⠸⢸⠁⠉⠙⠹⢹⠃⠋⠛⠻⢻⠇⠏⠟⠿⢿⡇⡏⡟⡿⣿"
		} else {
			symbols = " ⢀⢠⢰⢸⡀⣀⣠⣰⣸⡄⣄⣤⣴⣼⡆⣆⣦⣶⣾⡇⣇⣧⣷⣿"
		}
	case "block":
		if invert {
			symbols = " ▝▝▐▐▘▀▀▜▜▘▀▀▜▜▌▛▛██▌▛▛██"
		} else {
			symbols = " ▗▗▐▐▖▄▄▟▟▖▄▄▟▟▌▙▙██▌▙▙██"
		}
	default:
		symbols = " ░░▒▒░░▒▒█░▒▒▒█▒▒▒██▒████"
	}
	return []rune(symbols)
}
