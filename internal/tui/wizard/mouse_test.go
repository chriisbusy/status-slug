package wizard

import (
	"strings"
	"testing"

	"github.com/chriisbusy/status-slug/internal/config"
)

func TestFormLocalMatchesRenderedPopupGeometry(t *testing.T) {
	model := New(config.Default(), "")
	model.width, model.height = 100, 37
	headerHeight := strings.Count(model.header(), "\n") + 1
	boxWidth := model.formWidth() + 6
	boxHeight := model.formHeight() + headerHeight + 5
	startX := max(0, (model.width-boxWidth)/2)
	startY := max(0, (model.height-boxHeight)/2)
	x, y := model.formLocal(startX+3, startY+1+headerHeight+1)
	if x != 0 || y != 0 {
		t.Fatalf("form origin maps to (%d,%d), want (0,0)", x, y)
	}
}
