// Package shrink reduces a failing input to a minimal one with delta
// debugging (Zeller's ddmin).
package shrink

import "slices"

// Minimize returns a 1-minimal subsequence of items for which fails is still
// true: removing any single remaining element makes it pass. Order is kept.
// It tries large chunks first, then smaller ones, down to single elements.
// fails(items) must be true on entry. If fails returns an error, Minimize
// returns that error along with the smallest failing input found so far.
func Minimize[T any](items []T, fails func([]T) (bool, error)) ([]T, error) {
	n := 2
	for len(items) >= 2 {
		chunks := split(items, n)
		reduced := false

		// Does one chunk alone still fail?
		for _, c := range chunks {
			ok, err := fails(c)
			if err != nil {
				return slices.Clone(items), err
			}
			if ok {
				items, n, reduced = c, 2, true
				break
			}
		}

		// Does removing one chunk still fail? (With n == 2 the complements
		// are the chunks we just tried.)
		if !reduced && n > 2 {
			for i := range chunks {
				comp := complement(chunks, i)
				ok, err := fails(comp)
				if err != nil {
					return slices.Clone(items), err
				}
				if ok {
					items, n, reduced = comp, max(n-1, 2), true
					break
				}
			}
		}

		if !reduced {
			if n >= len(items) {
				break // every single element was necessary
			}
			n = min(n*2, len(items))
		}
	}
	return slices.Clone(items), nil
}

func split[T any](items []T, n int) [][]T {
	chunks := make([][]T, 0, n)
	size, rem := len(items)/n, len(items)%n
	start := 0
	for i := 0; i < n; i++ {
		end := start + size
		if i < rem {
			end++
		}
		chunks = append(chunks, items[start:end])
		start = end
	}
	return chunks
}

func complement[T any](chunks [][]T, skip int) []T {
	var out []T
	for i, c := range chunks {
		if i != skip {
			out = append(out, c...)
		}
	}
	return out
}
