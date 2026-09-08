package mcp_test

// aihub#449 / aihub#411 T2-20 — the ready queue's section count is ONE number,
// written in three places, and this test is what keeps the three the same.
//
// The three copies are:
//
//	struct       internal/domain/work_items.go (ReadyQueue) — the field list
//	schema       the pf_get_ready_queue description in tools_lifecycle.go
//	design doc   the Ready Queue block of docs/design/polyforge-v1-design.md
//
// Before this wi they said seven, six and six-with-three-impossible-fields
// respectively, and nothing anywhere went red. Each copy is individually
// correct-looking — that is the whole difficulty — so the only thing that can
// catch the drift is an assertion that reads all three and compares them.
//
// 🔴 Why the STRUCT is the authority and not the schema: the struct is what
// marshals, so it is the only one of the three a caller can observe. The other
// two are descriptions of it, and a description that disagrees with what ships
// is the defect this test names.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestReadyQueueSection

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	// readyQueueStructFile is the marshalling authority, relative to this
	// package's directory (where `go test` runs).
	readyQueueStructFile = "../domain/work_items.go"
	// readyQueueDesignDoc holds the third copy.
	readyQueueDesignDoc = "../../docs/design/polyforge-v1-design.md"
	// readyQueueDesignHeading opens the block inside it. The block is fenced, so
	// the heading plus the fence bounds it without any line numbers — docs/ may
	// not carry those (scripts/pf_docs_contract_check.py, C1).
	readyQueueDesignHeading = "#### Ready Queue (Layer 3)"
	// readyQueueSegmentFloor guards against the AST walk finding nothing: a
	// struct this test could not parse would otherwise agree with a description
	// that named no count, and every arm below would be vacuous. Measured
	// 2026-09-08: 7 segments.
	readyQueueSegmentFloor = 6
)

// readyQueueDeadFields are the four the design doc drew and no code could
// produce: `unblocked_at` had no writer anywhere (deleted from ReadyItem by
// aihub#449), `expires_at` went with the ownership model in v1.21, `kind` was
// deleted from work_items in v1.22, and `owner_user_type` — the one aihub#411
// T2-20 did not list — has never existed on RunningItem at all, zero hits in Go
// across the tree. They are listed here so that putting one back is a decision
// someone has to make in this file, rather than a paragraph somebody re-adds to
// a design doc nothing checks.
var readyQueueDeadFields = []string{"unblocked_at", "expires_at", "kind", "owner_user_type"}

// readyQueueSegments returns the json names of ReadyQueue's segment fields, in
// declaration order.
//
// A SEGMENT is a slice of an *Item type. That discriminator is what separates
// the six-plus-one queue sections from `request_adjusted`, which is a slice too
// but is a disclosure envelope rather than a section of the queue — and the
// distinction matters, because request_adjusted KEEPS its `omitempty` on purpose
// (internal/domain/request_adjusted.go) while no segment may carry one.
func readyQueueSegments(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, readyQueueStructFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", readyQueueStructFile, err)
	}

	var st *ast.StructType
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if ok && ts.Name.Name == "ReadyQueue" {
			if s, ok := ts.Type.(*ast.StructType); ok {
				st = s
			}
		}
		return st == nil
	})
	if st == nil || st.Fields == nil {
		t.Fatalf("ReadyQueue not found in %s — this test cannot pass by not finding its "+
			"subject", readyQueueStructFile)
	}

	var segments []string
	for _, fld := range st.Fields.List {
		arr, ok := fld.Type.(*ast.ArrayType)
		if !ok || arr.Len != nil {
			continue
		}
		elem, ok := arr.Elt.(*ast.Ident)
		if !ok || !strings.HasSuffix(elem.Name, "Item") {
			continue
		}
		if fld.Tag == nil {
			t.Errorf("ReadyQueue has an untagged segment field %v — it would marshal under "+
				"its Go name, which is not the published key", fld.Names)
			continue
		}
		tag := strings.Trim(fld.Tag.Value, "`")
		jsonTag := reflectStructTag(tag, "json")
		name, opts, _ := strings.Cut(jsonTag, ",")
		if strings.Contains(opts, "omitempty") {
			t.Errorf("ReadyQueue.%v (json %q) carries `omitempty`. A SEGMENT may not: its "+
				"absence would then mean both \"this section is empty\" and \"this server "+
				"predates the section\", and the empty case is a fact the Orchestrator acts "+
				"on. That is the rule internal/domain/request_adjusted.go states — an absent "+
				"key is acceptable only while the absence asserts NOTHING — and it is why "+
				"stale_running lost its omitempty in aihub#449 while request_adjusted keeps "+
				"one. Initialise the slice in newReadyQueue instead; a nil slice marshals to "+
				"`null`, which is worse than either.", fld.Names, name)
		}
		segments = append(segments, name)
	}

	if len(segments) < readyQueueSegmentFloor {
		t.Fatalf("only %d ReadyQueue segment(s) found, floor is %d — the walk is broken and "+
			"every comparison below would be vacuous", len(segments), readyQueueSegmentFloor)
	}
	return segments
}

// reflectStructTag pulls one key out of a struct tag without dragging in
// reflect.StructTag's whole surface. The tags here are the plain
// `json:"..."` shape and nothing else.
func reflectStructTag(tag, key string) string {
	for _, part := range strings.Fields(tag) {
		prefix := key + ":\""
		if strings.HasPrefix(part, prefix) {
			return strings.TrimSuffix(strings.TrimPrefix(part, prefix), "\"")
		}
	}
	return ""
}

// TestReadyQueueSectionCountIsOneNumber compares the schema's count with the
// struct's.
func TestReadyQueueSectionCountIsOneNumber(t *testing.T) {
	segments := readyQueueSegments(t)

	tools := liveContract(t)
	rq, ok := tools[readyQueueTool]
	if !ok {
		t.Fatalf("%s is not published — this test cannot pass by not finding its subject",
			readyQueueTool)
	}

	m := regexp.MustCompile(`\((\d+)-section\)`).FindStringSubmatch(rq.Description)
	if m == nil {
		t.Fatalf("%s's description states no section count: %q.\nIt has said one since the "+
			"tool was added, and the count is the half a caller plans around — dropping it "+
			"silently is how the schema and the struct got to disagree in the first place "+
			"(aihub#411 T2-20). Write \"(%d-section)\".", readyQueueTool, rq.Description,
			len(segments))
	}
	said, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("unreadable section count %q in %s's description", m[1], readyQueueTool)
	}
	if said != len(segments) {
		t.Errorf("%s's description says %d sections; internal/domain/work_items.go "+
			"(ReadyQueue) marshals %d: %s.\nThe struct is the authority — it is the one a "+
			"caller can observe — so fix the description, and fix the Ready Queue block of "+
			"docs/design/polyforge-v1-design.md in the same change.",
			readyQueueTool, said, len(segments), strings.Join(segments, ", "))
	}
	t.Logf("ready queue: %d segments (%s), description says %d",
		len(segments), strings.Join(segments, ", "), said)
}

// TestReadyQueueDesignDocDrawsTheSameSegments is the third copy: the design
// doc's response sketch, which is what a reader planning a Layer 3 orchestrator
// reads before they ever see the struct. aihub#186's design was written from it.
func TestReadyQueueDesignDocDrawsTheSameSegments(t *testing.T) {
	segments := readyQueueSegments(t)

	raw, err := os.ReadFile(filepath.Clean(readyQueueDesignDoc))
	if err != nil {
		t.Fatalf("read %s: %v", readyQueueDesignDoc, err)
	}
	block, ok := readyQueueDesignBlock(string(raw))
	if !ok {
		t.Fatalf("%q not found in %s, or it is not followed by a fenced block — retarget this "+
			"test rather than letting it pass by finding nothing",
			readyQueueDesignHeading, readyQueueDesignDoc)
	}

	for _, seg := range segments {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(seg) + `\s*:`).MatchString(block) {
			t.Errorf("the Ready Queue block of %s does not draw the %q segment, which "+
				"internal/domain/work_items.go (ReadyQueue) marshals on every response. A "+
				"reader plans against this sketch: aihub#186's orchestrator design was "+
				"written from it.", readyQueueDesignDoc, seg)
		}
	}
	for _, dead := range readyQueueDeadFields {
		if regexp.MustCompile(`(?m)^[^-]*\b` + regexp.QuoteMeta(dead) + `\b`).MatchString(block) {
			t.Errorf("the Ready Queue block of %s draws %q outside a comment. No code in "+
				"this repository can produce it: unblocked_at never had a writer, expires_at "+
				"went with the ownership model in v1.21, kind was deleted in v1.22, and "+
				"owner_user_type has never been a field on RunningItem. Drawing "+
				"a field that cannot exist is how aihub#186 got planned against a no-op "+
				"(aihub#387, aihub#449).", readyQueueDesignDoc, dead)
		}
	}
}

// readyQueueDesignBlock returns the fenced block that follows the Ready Queue
// heading. Bounded by the heading and the fence rather than by line numbers,
// which docs/ may not carry and this test may not depend on.
func readyQueueDesignBlock(doc string) (string, bool) {
	_, after, found := strings.Cut(doc, readyQueueDesignHeading)
	if !found {
		return "", false
	}
	_, after, found = strings.Cut(after, "```")
	if !found {
		return "", false
	}
	block, _, found := strings.Cut(after, "```")
	if !found {
		return "", false
	}
	return block, true
}
