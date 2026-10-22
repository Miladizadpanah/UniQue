package controller

import (
	"reflect"
	"testing"
)

// TestProjectedBeyondAnchor pins the rule that decides when a membership write
// must be followed by a debounced Xray restart: only when the reconcile set
// carries an inbound the write did not hot-add (i.e. anything but the anchor).
func TestProjectedBeyondAnchor(t *testing.T) {
	cases := []struct {
		name     string
		anchor   int
		set      []int
		wantBool bool
	}{
		{"single-inbound write stays hot, no restart", 8, []int{8}, false},
		{"empty set", 8, nil, false},
		{"membership on a second inbound needs a restart", 8, []int{8, 9}, true},
		{"second inbound listed first", 8, []int{9, 8}, true},
		{"duplicates only on the anchor stay hot", 8, []int{8, 8}, false},
		{"anchor 0 means nothing was hot-added", 0, []int{8}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectedBeyondAnchor(tc.anchor, tc.set); got != tc.wantBool {
				t.Fatalf("projectedBeyondAnchor(%d, %v) = %v, want %v", tc.anchor, tc.set, got, tc.wantBool)
			}
		})
	}

	// The union feeding the check: order-insensitive, duplicate-free, so the rule
	// cannot be tricked by how the caller merges membership and projection lists.
	if got := unionInboundIds([]int{8, 9}, []int{9, 10}); !reflect.DeepEqual(got, []int{8, 9, 10}) {
		t.Fatalf("unionInboundIds = %v, want [8 9 10]", got)
	}
}
