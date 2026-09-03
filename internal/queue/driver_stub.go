package queue

import (
	// SQLite driver retained as a direct module dependency before M4
	// implements the durable queue. See docs/adr/0001-sqlite-driver.md.
	_ "modernc.org/sqlite"
)
