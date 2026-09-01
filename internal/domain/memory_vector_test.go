package domain

import (
	"strings"
	"testing"
)

// TestEmbeddableType verifies embeddableType returns true for the three embeddable
// prefixes and false for methodology.* and unrecognized types.
func TestEmbeddableType(t *testing.T) {
	tests := []struct {
		memType string
		want    bool
	}{
		{"experience.debug", true},
		{"experience.approach", true},
		{"experience.pitfall", true},
		{"experience.code", true},
		{"fact.architecture", true},
		{"fact.constraint", true},
		{"fact.reference", true},
		{"fact.note", true},
		{"rule.scheduling", true},
		{"rule.convention", true},
		{"rule.process", true},
		{"rule.coding", true},
		{"rule.work", true},
		// methodology types must NOT be embedded
		{"methodology.spec", false},
		{"methodology.plan", false},
		{"methodology.review", false},
		{"methodology.execute", false},
		{"methodology.retro", false},
		{"methodology.wrap_summary", false},
		// unknown / empty
		{"", false},
		{"unknown.type", false},
	}
	for _, tc := range tests {
		got := embeddableType(tc.memType)
		if got != tc.want {
			t.Errorf("embeddableType(%q) = %v, want %v", tc.memType, got, tc.want)
		}
	}
}

// TestVecToPGLiteral verifies the pgvector literal encoding.
func TestVecToPGLiteral(t *testing.T) {
	tests := []struct {
		name string
		vec  []float32
		want string
	}{
		{
			name: "empty slice",
			vec:  []float32{},
			want: "[]",
		},
		{
			name: "nil slice",
			vec:  nil,
			want: "[]",
		},
		{
			name: "single element",
			vec:  []float32{0.5},
			want: "[0.5]",
		},
		{
			name: "multiple elements",
			vec:  []float32{0.1, 0.2, 0.3},
			want: "[0.1,0.2,0.3]",
		},
		{
			name: "negative values",
			vec:  []float32{-0.5, 0.0, 0.5},
			want: "[-0.5,0,0.5]",
		},
		{
			name: "precision preserved (float32 boundary)",
			vec:  []float32{1.0 / 3.0},
			// strconv.FormatFloat with 'f',-1,32 gives shortest float32 representation
			want: "[0.33333334]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := vecToPGLiteral(tc.vec)
			if got != tc.want {
				t.Errorf("vecToPGLiteral(%v) = %q, want %q", tc.vec, got, tc.want)
			}
		})
	}
}

// TestVecToPGLiteralBrackets verifies structural invariants for arbitrary length vectors.
func TestVecToPGLiteralBrackets(t *testing.T) {
	vec := make([]float32, 100)
	for i := range vec {
		vec[i] = float32(i) / 100
	}
	lit := vecToPGLiteral(vec)
	if !strings.HasPrefix(lit, "[") || !strings.HasSuffix(lit, "]") {
		t.Errorf("vecToPGLiteral: missing brackets: %q", lit)
	}
	inner := lit[1 : len(lit)-1]
	parts := strings.Split(inner, ",")
	if len(parts) != 100 {
		t.Errorf("vecToPGLiteral: expected 100 elements, got %d", len(parts))
	}
}
