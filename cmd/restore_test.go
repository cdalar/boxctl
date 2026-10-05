package cmd

import (
	"testing"

	"github.com/cdalar/boxctl/internal/client"
)

func TestFindBackup(t *testing.T) {
	// Newest first, as listBackups returns them.
	backups := []client.Backup{
		{ID: "b3", VMName: "web"},
		{ID: "b2", VMName: "db"},
		{ID: "b1", VMName: "web"},
		// A box named like another backup's id: the id wins.
		{ID: "b0", VMName: "b1"},
	}
	for arg, want := range map[string]string{"b2": "b2", "web": "b3", "db": "b2", "b1": "b1"} {
		got, err := findBackup(backups, arg)
		if err != nil || got.ID != want {
			t.Errorf("findBackup(%q) = %q, %v; want %q", arg, got.ID, err, want)
		}
	}
	if _, err := findBackup(backups, "nope"); err == nil {
		t.Error("findBackup(nope) found a backup")
	}
}
