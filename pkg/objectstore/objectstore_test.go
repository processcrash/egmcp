package objectstore

import "testing"

func TestLimit(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 1000},
		{-5, 1000},
		{50, 50},
		{20_000, 10_000},
	}
	for _, c := range cases {
		if got := Limit(c.in); got != c.want {
			t.Fatalf("Limit(%d): want %d, got %d", c.in, c.want, got)
		}
	}
}

func TestPrefixMust(t *testing.T) {
	if err := PrefixMust(""); err != nil {
		t.Fatalf("empty prefix: %v", err)
	}
	if err := PrefixMust("anything"); err != nil {
		t.Fatalf("non-empty prefix: %v", err)
	}
}
