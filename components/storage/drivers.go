// Driver registrations for every SQL sink this application can target.
// database/sql drivers register themselves via init; importing them here
// keeps components/storage self-contained so a configured backend is always
// usable without per-binary wiring. 全部为纯 Go 实现，Windows 无 CGO 依赖。
package storageplugin

import (
	_ "github.com/go-sql-driver/mysql"  // mysql-storage, registers "mysql"
	_ "github.com/jackc/pgx/v5/stdlib"  // postgresql-storage, registers "pgx"
	_ "github.com/microsoft/go-mssqldb" // sqlserver-storage, registers "sqlserver"
	_ "github.com/sijms/go-ora"         // oracle-storage, registers "oracle"
	_ "modernc.org/sqlite"              // sqlite-storage, registers "sqlite"
)
