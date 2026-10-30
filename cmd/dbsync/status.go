package main

import (
	"context"
	"fmt"

	"github.com/MKwann7/db-sync-specific-files/internal/state"
)

func runStatusCommand(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	db, err := openState(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	all, err := db.All(context.Background())
	if err != nil {
		return fmt.Errorf("reading synchronization state: %w", err)
	}

	if len(all) == 0 {
		fmt.Println("No synchronization history yet.")
		return nil
	}

	fmt.Printf("%-40s %-12s %-24s %s\n", "MAPPING (remote path)", "STATUS", "LAST SYNCED", "LAST ERROR")
	for _, ms := range all {
		lastSynced := "never"
		if ms.LastSyncedAt != nil {
			lastSynced = ms.LastSyncedAt.Format("2006-01-02T15:04:05Z")
		}
		if ms.Status == state.StatusInProgress && ms.InProgressSince != nil {
			lastSynced = "interrupted at " + ms.InProgressSince.Format("2006-01-02T15:04:05Z")
		}
		fmt.Printf("%-40s %-12s %-24s %s\n", ms.MappingID, ms.Status, lastSynced, ms.LastError)
	}
	return nil
}
