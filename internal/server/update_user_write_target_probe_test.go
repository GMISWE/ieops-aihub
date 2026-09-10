package server

// aihub#591 — pf_update_user's isolation sentence, pinned: "Changing it does not
// touch `projects.members`."
//
// The two role vocabularies are distinct surfaces (users.role vs the member
// ladder in projects.members), and the card's arms held the VOCABULARY split
// while nothing held the WRITE split — a future "helpfully" cascading role
// change into project memberships would leave every existing arm green. The pin
// is on the handler's source: its one statement writes the users table, and the
// function reaches no projects relation at all.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestUpdateUserWritesTheUsersTableOnly(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	marker := "\nfunc handleUpdateUser("
	i := strings.Index(string(raw), marker)
	if i < 0 {
		t.Fatalf("router.go no longer declares handleUpdateUser — if the handler moved, " +
			"move this pin with it")
	}
	rest := string(raw)[i+len(marker):]
	if loc := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}

	if !strings.Contains(rest, `"UPDATE users SET "`) {
		t.Errorf("handleUpdateUser no longer builds its statement on \"UPDATE users SET\". " +
			"The card publishes that a role change touches the users row and nothing else; " +
			"if the write moved, re-read pf_update_user.md's two-vocabulary section before " +
			"changing this pin.")
	}
	if strings.Contains(rest, "projects") {
		t.Errorf("handleUpdateUser now mentions a projects relation. \"Changing it does not " +
			"touch projects.members\" is a published card sentence — a cascade from the " +
			"global role into member rows is a contract change, not an implementation " +
			"detail, and both role ladders' cards have to move with it.")
	}
}
