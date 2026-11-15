package page_test

import (
	"testing"

	"github.com/mettjs/cairnpost/internal/page"
)

func TestAPageHasASizeWhateverIsAsked(t *testing.T) {
	for asked, want := range map[int]int{-1: page.Default, 0: page.Default, 1: 1, page.Most: page.Most,
		page.Hard: page.Hard, page.Hard + 1: page.Hard} {
		if got := page.Size(asked); got != want {
			t.Errorf("asked %d: a page of %d, want %d", asked, got, want)
		}
	}
}
