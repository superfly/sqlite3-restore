sqlite3-restore
===============

This is a repository for the `sqlite3-restore` utility that provides a way to
restore a SQLite database online while other clients are connected. It acquires
the necessary locks on the destination database before performing a file copy.
This preserves the inodes so that existing clients can continue to read and
write from the database after the copy is complete.
