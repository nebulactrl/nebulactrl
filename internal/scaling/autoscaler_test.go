package scaling

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

// at is a fixed instant aligned to the minute so bucket maths is exact.
var at = time.Date(2026, 10, 26, 12, 0, 20, 0, time.UTC)

// series returns n buckets of width step that end at the last boundary before
// at, all with the same CPU, oldest first.
func series(n int, step time.Duration, cpu float64) []Bucket {
	end := at.Truncate(step)
	out := make([]Bucket, 0, n)
	for i := n; i >= 1; i-- {
		out = append(out, Bucket{Start: end.Add(-time.Duration(i) * step), CPU: cpu})
	}
	return out
}

func input() DecideInput {
	return DecideInput{Now: at, Replicas: 2, Auto: services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70}}
}

func TestDecideScalesOutAfterTwoMinutesAboveTarget(t *testing.T) {
	in := input()
	in.OutBuckets = series(4, outStep, 84)
	d := Decide(in)
	require.Equal(t, ScaleOut, d.Direction)
	require.Equal(t, "CPU 84% > 70% for 2m", d.Reason)

	// Only 90 seconds above target: not yet.
	in.OutBuckets = series(3, outStep, 84)
	require.Equal(t, Hold, Decide(in).Direction)

	// A dip below the target inside the window resets it.
	in.OutBuckets = series(4, outStep, 84)
	in.OutBuckets[1].CPU = 50
	require.Equal(t, Hold, Decide(in).Direction)

	// At the target exactly is not above it.
	in.OutBuckets = series(4, outStep, 70)
	require.Equal(t, Hold, Decide(in).Direction)
}

func TestDecideMeasuresAgainstTheCPULimit(t *testing.T) {
	in := input()
	in.OutBuckets = series(4, outStep, 40) // 40% of one core
	require.Equal(t, Hold, Decide(in).Direction, "no limit: one core")
	in.CPULimit = 0.5 // 40% of a core is 80% of the half core limit
	d := Decide(in)
	require.Equal(t, ScaleOut, d.Direction)
	require.Equal(t, "CPU 80% > 70% for 2m", d.Reason)
}

func TestDecideScalesInAfterTenMinutesBelowThreshold(t *testing.T) {
	in := input()
	in.InBuckets = series(10, inStep, 22)
	d := Decide(in)
	require.Equal(t, ScaleIn, d.Direction)
	require.Equal(t, "CPU 22% < 35% for 10m", d.Reason)

	in.InBuckets = series(9, inStep, 22)
	require.Equal(t, Hold, Decide(in).Direction, "nine minutes are not enough")

	in.InBuckets = series(10, inStep, 36)
	require.Equal(t, Hold, Decide(in).Direction, "36% is above the threshold")

	in.InBuckets = series(10, inStep, 22)
	in.Replicas = 1
	require.Equal(t, Hold, Decide(in).Direction, "never below min")
}

func TestDecideStaysWithinBounds(t *testing.T) {
	in := input()
	in.Replicas = 3
	in.OutBuckets = series(4, outStep, 99)
	require.Equal(t, Hold, Decide(in).Direction, "never above max")
}

func TestDecideRespectsCooldowns(t *testing.T) {
	in := input()
	in.OutBuckets = series(4, outStep, 90)
	in.InBuckets = series(10, inStep, 10)

	last := at.Add(-2 * time.Minute)
	in.LastScale, in.LastScaledUp = &last, true
	require.Equal(t, Hold, Decide(in).Direction, "3 minute cooldown after scaling out")
	last = at.Add(-3*time.Minute - time.Second)
	require.Equal(t, ScaleOut, Decide(in).Direction)

	last = at.Add(-9 * time.Minute)
	in.LastScaledUp = false
	require.Equal(t, Hold, Decide(in).Direction, "10 minute cooldown after scaling in")
	last = at.Add(-10*time.Minute - time.Second)
	require.Equal(t, ScaleOut, Decide(in).Direction)
}

func TestScaleInThresholdNeverExceedsHalfTheTarget(t *testing.T) {
	require.InDelta(t, 35, ScaleInThreshold(70), 0.001)
	require.InDelta(t, 35, ScaleInThreshold(95), 0.001)
	require.InDelta(t, 20, ScaleInThreshold(40), 0.001)
}
