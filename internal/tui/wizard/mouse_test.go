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

func TestIdentityFormCompletesFromFinalNote(t *testing.T) {
	model := New(config.Default(), "")
	model.data.name = "ReviewProvider"
	model.data.presetSel = "Custom"
	model.data.baseURL = "http://127.0.0.1:1/v1"
	model.enterIdentity()
	for range 6 {
		if model.form.HandleKey("tab") {
			t.Fatal("identity form completed before final field")
		}
	}
	if !model.form.HandleKey("enter") {
		t.Fatal("Enter on final note did not complete identity form")
	}
}

func TestIdentityCompletionTransitionsLiveModel(t *testing.T) {
	model := New(config.Default(), "")
	for range 6 {
		model.form.HandleKey("tab")
	}
	if !model.form.HandleKey("enter") {
		t.Fatal("identity form did not complete")
	}
	_, _ = model.stepCompleted()
	if model.step != stepKeySource {
		t.Fatalf("identity completion left step=%v, want key source", model.step)
	}
}

func TestReviewShiftTabReturnsToMeters(t *testing.T) {
	model := New(config.Default(), "")
	model.data.name = "provider"
	model.enterSummary()
	if !model.form.AtFirst() {
		t.Fatal("summary should start on its sole button row")
	}
	_, _ = model.previousStep()
	if model.step != stepMeters {
		t.Fatalf("review back left step=%v, want meters", model.step)
	}
}
