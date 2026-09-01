package widgets

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/chriisbusy/status-slug/internal/theme"
)

func TestTextCursorBlinkNeverChangesPromptLabel(t *testing.T) {
	palette, _ := theme.Load("sstop", "")
	field := NewText(palette, "what do you call it?", "example")
	field.Focus()
	beforeView := field.View(50)
	field.Tick()
	afterView := field.View(50)
	before := strings.Split(beforeView, "\n")[0]
	after := strings.Split(afterView, "\n")[0]
	if before != after {
		t.Fatalf("cursor blink changed prompt label: before=%q after=%q", before, after)
	}
	for name, view := range map[string]string{"before": beforeView, "after": afterView} {
		if !strings.Contains(view, "example") {
			t.Fatalf("%s cursor frame hid placeholder: %q", name, view)
		}
	}
}

func TestSelectCarouselShowsOneFullWidthSelection(t *testing.T) {
	palette, _ := theme.Load("sstop", "")
	field := NewSelect(palette, "theme", []string{"sstop", "nord", "mono"})
	field.Focus()
	view := field.View(30)
	if strings.Contains(view, "nord") || strings.Contains(view, "mono") || strings.ContainsAny(view, ">◆◇") {
		t.Fatalf("carousel leaked options or markers: %q", view)
	}
	for row, line := range strings.Split(view, "\n")[:2] {
		if got := ansi.StringWidth(line); got != 30 {
			t.Fatalf("selected row %d width=%d, want 30: %q", row, got, line)
		}
	}
}

func TestConfirmEnterAcceptsWithoutToggling(t *testing.T) {
	palette, _ := theme.Load("sstop", "")
	field := NewConfirm(palette, "add meter?", false)
	changed, submit := field.HandleKey("enter")
	if !changed || !submit || field.Value {
		t.Fatalf("Enter changed false selection: changed=%v submit=%v value=%v", changed, submit, field.Value)
	}
	field.HandleKey("right")
	field.Value = false
	changed, submit = field.HandleKey("space")
	if !changed || submit || !field.Value {
		t.Fatalf("space did not toggle confirmation: changed=%v submit=%v value=%v", changed, submit, field.Value)
	}
	if !field.Value {
		t.Fatal("right did not toggle confirmation")
	}
}
