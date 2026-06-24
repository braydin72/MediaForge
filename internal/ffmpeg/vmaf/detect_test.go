package vmaf

import "testing"

func TestIsAvailable(t *testing.T) {
	// Save and restore global state
	oldAvailable := vmafAvailable
	defer func() { vmafAvailable = oldAvailable }()

	vmafAvailable = true
	if !IsAvailable() {
		t.Error("IsAvailable() = false, want true")
	}

	vmafAvailable = false
	if IsAvailable() {
		t.Error("IsAvailable() = true, want false")
	}
}

func TestGetModels(t *testing.T) {
	// Save and restore global state
	oldModels := vmafModels
	defer func() { vmafModels = oldModels }()

	vmafModels = []string{"model1", "model2"}
	got := GetModels()
	if len(got) != 2 || got[0] != "model1" || got[1] != "model2" {
		t.Errorf("GetModels() = %v, want [model1, model2]", got)
	}
}
