package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Database file lock bytes.
const (
	PENDING  = 0x40000000
	RESERVED = 0x40000001
	SHARED   = 0x40000002
)

// SHM file lock bytes.
const (
	WRITE   = 120
	CKPT    = 121
	RECOVER = 122
	READ0   = 123
	READ1   = 124
	READ2   = 125
	READ3   = 126
	READ4   = 127
	DMS     = 128
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	timeout := flag.Duration("timeout", 5*time.Second, "lock timeout")
	flag.Parse()
	if flag.NArg() != 2 {
		return fmt.Errorf("usage: sqlite3-restore SRC DST")
	}
	src, dst := flag.Arg(0), flag.Arg(1)

	// Open destination database file.
	dstDBFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return err
	}
	defer func() { _ = dstDBFile.Close() }()

	// Set timeout on the lock.
	lockCtx := context.Background()
	if *timeout > 0 {
		ctx, cancel := context.WithTimeout(lockCtx, *timeout)
		defer cancel()
		lockCtx = ctx
	}

	// Acquire all locks required for exclusive access.
	var dstSHMFile *os.File
	if err := lockAll(lockCtx, dstDBFile, &dstSHMFile); err != nil {
		return err
	}
	if dstSHMFile != nil {
		defer func() { _ = dstSHMFile.Close() }()
	}

	// Remove the journal file if one exists.
	if err := os.Remove(dst + "-journal"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove journal file: %w", err)
	}

	// If this is WAL mode, truncate the WAL file.
	if dstSHMFile != nil {
		if err := os.Truncate(dst+"-wal", 0); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("truncate wal: %w", err)
		}
	}

	// Copy from src
	srcDBFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcDBFile.Close() }()

	fi, err := srcDBFile.Stat()
	if err != nil {
		return err
	}

	if _, err := dstDBFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := srcDBFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(dstDBFile, srcDBFile); err != nil {
		return fmt.Errorf("copy database: %w", err)
	}
	if err := dstDBFile.Truncate(fi.Size()); err != nil {
		return fmt.Errorf("set destination database size: %w", err)
	}
	if err := dstDBFile.Sync(); err != nil {
		return fmt.Errorf("sync database: %w", err)
	}

	// Invalidate SHM.
	if dstSHMFile != nil {
		if _, err := dstSHMFile.WriteAt(make([]byte, 136), 0); err != nil {
			return fmt.Errorf("invalidate shm file: %w", err)
		}
	}

	// Fsync parent directory.
	dir, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("directory sync: %w", err)
	}

	// Close file handles to release locks.
	if err := srcDBFile.Close(); err != nil {
		return fmt.Errorf("close source database: %w", err)
	}
	if err := dstDBFile.Close(); err != nil {
		return fmt.Errorf("close destination database: %w", err)
	}
	if dstSHMFile != nil {
		if err := dstSHMFile.Close(); err != nil {
			return fmt.Errorf("close destination shm file: %w", err)
		}
	}

	return nil
}

func lockAll(ctx context.Context, dbFile *os.File, shmFile **os.File) error {
	// Acquire shared lock database file to determine mode.
	if err := lock(ctx, dbFile, syscall.F_RDLCK, PENDING); err != nil {
		return fmt.Errorf("acquire PENDING lock: %w", err)
	}
	if err := lock(ctx, dbFile, syscall.F_RDLCK, SHARED); err != nil {
		return fmt.Errorf("acquire SHARED read lock: %w", err)
	}
	if err := lock(ctx, dbFile, syscall.F_UNLCK, PENDING); err != nil {
		return fmt.Errorf("release PENDING lock: %w", err)
	}

	// Read mode from header.
	isWAL, err := isWALMode(dbFile)
	if err != nil {
		return fmt.Errorf("read mode: %w", err)
	}

	// If journal mode, upgrade to write locks.
	if !isWAL {
		if err := lock(ctx, dbFile, syscall.F_WRLCK, RESERVED); err != nil {
			return fmt.Errorf("acquire exclusive RESERVED lock: %w", err)
		}
		if err := lock(ctx, dbFile, syscall.F_WRLCK, PENDING); err != nil {
			return fmt.Errorf("acquire exclusive PENDING lock: %w", err)
		}
		if err := lock(ctx, dbFile, syscall.F_WRLCK, SHARED); err != nil {
			return fmt.Errorf("acquire exclusive SHARED lock: %w", err)
		}
		return nil
	}

	// If this is WAL mode then create the SHM file, if it doesn't exist.
	*shmFile, err = os.OpenFile(dbFile.Name()+"-shm", os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return err
	}

	// Then acquire all the SHM locks.
	if err := lock(ctx, *shmFile, syscall.F_RDLCK, DMS); err != nil {
		return fmt.Errorf("acquire shared DMS lock: %w", err)
	}
	if err := lock(ctx, *shmFile, syscall.F_WRLCK, WRITE); err != nil {
		return fmt.Errorf("acquire exclusive WRITE lock: %w", err)
	}
	if err := lock(ctx, *shmFile, syscall.F_WRLCK, CKPT); err != nil {
		return fmt.Errorf("acquire exclusive CKPT lock: %w", err)
	}
	if err := lock(ctx, *shmFile, syscall.F_WRLCK, RECOVER); err != nil {
		return fmt.Errorf("acquire exclusive RECOVER lock: %w", err)
	}
	if err := lock(ctx, *shmFile, syscall.F_WRLCK, READ0); err != nil {
		return fmt.Errorf("acquire exclusive READ0 lock: %w", err)
	}
	if err := lock(ctx, *shmFile, syscall.F_WRLCK, READ1); err != nil {
		return fmt.Errorf("acquire exclusive READ1 lock: %w", err)
	}
	if err := lock(ctx, *shmFile, syscall.F_WRLCK, READ2); err != nil {
		return fmt.Errorf("acquire exclusive READ2 lock: %w", err)
	}
	if err := lock(ctx, *shmFile, syscall.F_WRLCK, READ3); err != nil {
		return fmt.Errorf("acquire exclusive READ3 lock: %w", err)
	}
	if err := lock(ctx, *shmFile, syscall.F_WRLCK, READ4); err != nil {
		return fmt.Errorf("acquire exclusive READ4 lock: %w", err)
	}

	return nil
}

func lock(ctx context.Context, f *os.File, typ int16, byt int64) error {
	start, end := byt, byt
	if start == SHARED {
		end = SHARED + 510
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Attempt non-blocking lock until we are successful.
		println("dbg/lock", start, end)
		if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &syscall.Flock_t{
			Start:  start,
			Len:    end,
			Type:   typ,
			Whence: io.SeekStart,
		}); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// isWALMode returns true if the file format write version is 2 (WAL).
func isWALMode(f *os.File) (bool, error) {
	hdr := make([]byte, 100)
	if _, err := io.ReadFull(f, hdr); err == io.EOF || err == io.ErrUnexpectedEOF {
		return false, nil
	} else if err != nil {
		return false, err
	} else if hdr[18] != hdr[19] {
		return false, fmt.Errorf("database header write format (%d) does not match read format (%d)", hdr[18], hdr[19])
	}
	return hdr[18] == 2, nil
}
