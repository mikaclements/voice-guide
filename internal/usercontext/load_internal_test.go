package usercontext

import "testing"

func TestALoadReadsOneItemPer100CharactersOfItsLimitAndAtLeast20(t *testing.T) {
	for maxChars, want := range map[int]int{20_000: 200, 100_000: 1000, 1000: 20, 10: 20} {
		if got := rowsFor(maxChars); got != want {
			t.Errorf("rowsFor(%d) = %d, want %d", maxChars, got, want)
		}
	}
}
