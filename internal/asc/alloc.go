package asc

// The generated models nest anonymous structs behind pointers, e.g.
// BundleId.Attributes is *struct{...}, which cannot be named outside the
// generated code. Alloc and Append build them with the field types inferred,
// so the compiler still checks every field against the spec.

// Alloc sets *p to a new zero value when it is nil and returns it.
func Alloc[T any](p **T) *T {
	if *p == nil {
		*p = new(T)
	}
	return *p
}

// Append adds a zero element to the slice behind *p, allocating the slice
// when *p is nil, and returns a pointer to the new element.
func Append[E any](p **[]E) *E {
	s := Alloc(p)
	*s = append(*s, *new(E))
	return &(*s)[len(*s)-1]
}

// Value returns *p, or the zero value when p is nil.
func Value[T any](p *T) T {
	if p == nil {
		return *new(T)
	}
	return *p
}

// Push appends a zero element to *s and returns a pointer to it, for the
// generated slices of anonymous structs that are not behind a pointer.
func Push[E any](s *[]E) *E {
	*s = append(*s, *new(E))
	return &(*s)[len(*s)-1]
}
