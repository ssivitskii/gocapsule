package capsule

import "testing"

func TestGoroutineThresholdCrossingAndRearm(t *testing.T) {
	policy, err := NewGoroutineThreshold(10, 7)
	if err != nil {
		t.Fatal(err)
	}
	sequence := []struct {
		count int
		fire  bool
	}{{12, false}, {6, false}, {9, false}, {10, true}, {15, false}, {7, false}, {6, false}, {10, true}}
	for i, item := range sequence {
		if got := policy.Observe(item.count); got != item.fire {
			t.Fatalf("step %d Observe(%d) = %v, want %v", i, item.count, got, item.fire)
		}
	}
}

func TestGoroutineThresholdValidation(t *testing.T) {
	for _, values := range [][2]int{{0, 0}, {5, -1}, {5, 5}, {5, 6}} {
		if _, err := NewGoroutineThreshold(values[0], values[1]); err == nil {
			t.Fatalf("NewGoroutineThreshold(%d, %d) succeeded", values[0], values[1])
		}
	}
}
