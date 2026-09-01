package timeserieslimits

import (
	"bytes"
	"math"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompb"
)

// TestMustInitBigMaxLabelValueLen verifies that -maxLabelValueLen=1638400 is accepted
// at startup, while the effective runtime limit stays at math.MaxUint16 (64KiB).
func TestMustInitBigMaxLabelValueLen(t *testing.T) {
	MustInit(40, 256, 1638400)

	// A label value of exactly 64KiB must be accepted, since the effective limit is clamped to math.MaxUint16.
	labels := []prompb.Label{
		{Name: "long_value", Value: string(bytes.Repeat([]byte("v"), math.MaxUint16))},
	}
	if IsExceeding(labels) {
		t.Fatalf("series with a label value of length %d must be accepted", math.MaxUint16)
	}

	// A label value longer than 64KiB must still be rejected, as before this change.
	labels[0].Value = string(bytes.Repeat([]byte("v"), math.MaxUint16+1))
	if !IsExceeding(labels) {
		t.Fatalf("series with a label value longer than the effective 64KiB limit must be rejected")
	}
}
