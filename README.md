sqlite3-restore
===============

This is a repository for the `sqlite3-restore` utility that provides a way to
restore a SQLite database online while other clients are connected. It acquires
the necessary locks on the destination database before performing a file copy.
This preserves the inodes so that existing clients can continue to read and
write from the database after the copy is complete.


## Usage

The utility accepts a source database file and a destination database file:

```sh
sqlite3-restore SRC DST
```

If the destination database file does not exist, then it will be created. If it
does exist, the journal file will be removed, the WAL file truncated, and the
SHM file will be invalidated.

You can also set a timeout to wait on locks. By default, it is set to 5 seconds:

```sh
sqlite3-restore -timeout 30s SRC DST
```

## Caveats

The source and destination databases must have the same journal mode when
performing a restore. Otherwise, you may experience locking issues when restoring.

Please note that running `VACUUM INTO` will cause the output database to have a
rollback journal mode (`DELETE`) regardless of the journal mode of the database
you are vacuuming from.

You can update the journal mode using the `sqlite3` CLI:

```sh
sqlite3 /path/to/src "PRAGMA journal_mode = wal"
```
