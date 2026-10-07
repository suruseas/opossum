package orchestrator

import (
	"testing"
	"time"
)

// What the hint says about how long ago Docker built an image: the largest whole unit that is not nothing, one of it in the singular, from "less than a minute" to
// years, so that someone who knows how old the source is can tell whether it is the image to take.
func TestAgeWordsNameTheLargestWholeUnit(t *testing.T) {
	const day = 24 * time.Hour
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "less than a minute"},
		{59 * time.Second, "less than a minute"},
		{time.Minute, "1 minute"},
		{5*time.Minute + 59*time.Second, "5 minutes"},
		{59*time.Minute + 59*time.Second, "59 minutes"},
		{time.Hour, "1 hour"},
		{23*time.Hour + 59*time.Minute, "23 hours"},
		{day, "1 day"},
		{3*day + time.Hour, "3 days"},
		{29*day + 23*time.Hour, "29 days"},
		{30 * day, "1 month"},
		{364 * day, "12 months"},
		{365 * day, "1 year"},
		{800 * day, "2 years"},
	} {
		if got := ageWords(tc.ago); got != tc.want {
			t.Errorf("ageWords(%v) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}
