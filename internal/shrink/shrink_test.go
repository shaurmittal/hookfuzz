package shrink

import (
	"errors"
	"math/rand"
	"slices"
	"testing"
)

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func containsAll(want ...int) func([]int) (bool, error) {
	return func(xs []int) (bool, error) {
		for _, w := range want {
			if !slices.Contains(xs, w) {
				return false, nil
			}
		}
		return true, nil
	}
}

func TestMinimizeFindsASingleCause(t *testing.T) {
	got, err := Minimize(seq(40), containsAll(17))
	if err != nil || !slices.Equal(got, []int{17}) {
		t.Fatalf("Minimize = %v, %v; want [17]", got, err)
	}
}

func TestMinimizeFindsAPair(t *testing.T) {
	got, _ := Minimize(seq(10), containsAll(3, 7))
	if !slices.Equal(got, []int{3, 7}) {
		t.Fatalf("Minimize = %v, want [3 7]", got)
	}
}

func TestMinimizeKeepsOrder(t *testing.T) {
	sevenBeforeThree := func(xs []int) (bool, error) {
		i, j := slices.Index(xs, 7), slices.Index(xs, 3)
		return i >= 0 && j >= 0 && i < j, nil
	}
	got, _ := Minimize([]int{9, 7, 5, 3, 1}, sevenBeforeThree)
	if !slices.Equal(got, []int{7, 3}) {
		t.Fatalf("Minimize = %v, want [7 3]", got)
	}
}

func TestMinimizeIsOneMinimal(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 100; trial++ {
		var target []int
		for _, x := range rng.Perm(40)[:1+rng.Intn(4)] {
			target = append(target, x+1)
		}
		got, _ := Minimize(seq(40), containsAll(target...))
		slices.Sort(target)
		if !slices.Equal(got, target) {
			t.Fatalf("trial %d: Minimize = %v, want %v", trial, got, target)
		}
	}
}

func TestMinimizeTinyInputsSkipTheCheck(t *testing.T) {
	calls := 0
	count := func([]int) (bool, error) { calls++; return true, nil }
	if got, _ := Minimize([]int{}, count); len(got) != 0 {
		t.Errorf("Minimize([]) = %v", got)
	}
	if got, _ := Minimize([]int{4}, count); !slices.Equal(got, []int{4}) {
		t.Errorf("Minimize([4]) = %v", got)
	}
	if calls != 0 {
		t.Errorf("fails called %d times, want 0", calls)
	}
}

func TestMinimizePropagatesErrors(t *testing.T) {
	boom := errors.New("target down")
	_, err := Minimize(seq(8), func([]int) (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
