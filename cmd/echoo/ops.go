package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"echoo/internal/contacts"
	"echoo/internal/ops"
)

// rotateKeys re-encrypts every stored secret with the active key (the first entry of
// ECHOO_ENCRYPTION_KEYS) and then checks that the older keys are no longer used.
func rotateKeys(args []string) error {
	if len(args) > 0 {
		return errors.New("rotate-keys takes no arguments")
	}
	ctx := context.Background()
	a, err := setup(ctx)
	if err != nil {
		return err
	}
	defer a.pool.Close()
	return runRotateKeys(ctx, a, os.Stdout)
}

func runRotateKeys(ctx context.Context, a *app, out io.Writer) error {
	var writeErr error
	say := func(format string, args ...any) {
		if _, err := fmt.Fprintf(out, format, args...); err != nil {
			writeErr = errors.Join(writeErr, err)
		}
	}
	say("Active key: %s; configured keys: %s\n", a.keys.ActiveID(), strings.Join(a.keys.IDs(), ", "))
	rep, err := ops.Rotate(ctx, a.pool, a.keys, out)
	if err != nil {
		return err
	}
	usage, err := ops.CountKeyUsage(ctx, a.pool)
	if err != nil {
		return err
	}
	incomplete := rep.Failed > 0 || rep.Raced > 0
	retired := ops.RetiredKeys(a.keys, usage)
	for _, id := range ops.SortedKeys(retired) {
		if retired[id] == 0 {
			say("old key %s no longer used: it can be removed from ECHOO_ENCRYPTION_KEYS\n", id)
			continue
		}
		say("old key %s is still used by %d values\n", id, retired[id])
		incomplete = true
	}
	unusable := ops.UnusableKeys(a.keys, usage)
	for _, id := range ops.SortedKeys(unusable) {
		say("%d stored values use key %q, which is not configured: add it back to ECHOO_ENCRYPTION_KEYS\n", unusable[id], id)
		incomplete = true
	}
	if incomplete {
		return errors.Join(writeErr, fmt.Errorf("rotation incomplete: %d unreadable, %d changed meanwhile; fix the lines above and run it again", rep.Failed, rep.Raced))
	}
	say("Done: %d values re-encrypted.\n", rep.Rotated)
	if rep.Rotated > 0 {
		say("Satisfaction survey links in mail sent before the rotation stop working; see docs/operations.md.\n")
	}
	return writeErr
}

// adminBlobs compares the database with the blob store.
func adminBlobs(args []string) error {
	fs := flag.NewFlagSet("blobs", flag.ContinueOnError)
	check := fs.Bool("check", false, "report referenced blobs that are missing and, for filesystem storage, files nothing refers to")
	deleteOrphans := fs.Bool("delete-orphans", false, "delete files nothing refers to (filesystem storage)")
	yes := fs.Bool("yes", false, "confirm --delete-orphans; without it only the list is printed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *check == *deleteOrphans {
		return errors.New("give exactly one of --check and --delete-orphans")
	}
	if *yes && !*deleteOrphans {
		return errors.New("--yes only goes with --delete-orphans")
	}
	ctx := context.Background()
	a, err := setup(ctx)
	if err != nil {
		return err
	}
	defer a.pool.Close()

	rep, err := ops.CheckBlobs(ctx, a.pool, a.store, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("%d blobs referenced by the database\n", rep.Referenced)
	for _, m := range rep.Missing {
		fmt.Printf("MISSING  %s  (%s)\n", m.Key, m.Source)
	}
	if !rep.Walked {
		fmt.Println("This storage backend cannot be listed: files nothing refers to are not checked.")
	} else {
		fmt.Printf("%d files nothing refers to (%d bytes); %d files younger than %s were skipped\n",
			len(rep.Orphans), rep.OrphanBytes, rep.SkippedRecent, ops.OrphanGrace)
		for _, k := range rep.Orphans[:min(len(rep.Orphans), 20)] {
			fmt.Printf("ORPHAN   %s\n", k)
		}
		if len(rep.Orphans) > 20 {
			fmt.Printf("... and %d more\n", len(rep.Orphans)-20)
		}
	}

	if *check {
		if len(rep.Missing) > 0 || len(rep.Orphans) > 0 {
			return fmt.Errorf("%d missing, %d orphaned", len(rep.Missing), len(rep.Orphans))
		}
		return nil
	}
	if !rep.Walked {
		return errors.New("--delete-orphans needs filesystem storage")
	}
	if len(rep.Orphans) == 0 {
		return nil
	}
	if !*yes {
		return errors.New("nothing deleted; run again with --yes to delete the files above")
	}
	blobs, err := contacts.AsBlobs(a.store)
	if err != nil {
		return err
	}
	queued, failed, err := ops.DeleteOrphans(ctx, a.pool, blobs, rep.Orphans)
	if err != nil {
		return err
	}
	fmt.Printf("%d files handed to the reference-checked deletion queue; %d deletions failed\n", queued, failed)
	if failed > 0 {
		return fmt.Errorf("%d deletions failed and stay queued for the hourly purge job", failed)
	}
	return nil
}
