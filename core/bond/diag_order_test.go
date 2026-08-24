package bond

import "testing"

func TestSortPathDiagnosticsByID(t *testing.T) {
	paths := []PathDiag{{ID: 9}, {ID: 1}, {ID: 4}}
	sortPathDiagnostics(paths)
	want := []uint8{1, 4, 9}
	for i, id := range want {
		if paths[i].ID != id {
			t.Fatalf("path %d: got id %d, want %d", i, paths[i].ID, id)
		}
	}
}

func TestSortSessionDiagnosticsBySessionIndex(t *testing.T) {
	sessions := []SessionDiag{
		{SessionIndex: "000000ff"},
		{SessionIndex: "00000002"},
		{SessionIndex: "0000000a"},
	}
	sortSessionDiagnostics(sessions)
	want := []string{"00000002", "0000000a", "000000ff"}
	for i, sessionIndex := range want {
		if sessions[i].SessionIndex != sessionIndex {
			t.Fatalf("session %d: got %q, want %q", i, sessions[i].SessionIndex, sessionIndex)
		}
	}
}
