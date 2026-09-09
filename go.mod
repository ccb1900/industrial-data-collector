module gocordis-csv-collector

go 1.25.0

require (
<<<<<<< HEAD
	dynamic-runtime v0.0.0-20260909154652-ffdf556ba9a8
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/text v0.29.0
	modernc.org/sqlite v1.58.0
=======
	dynamic-runtime v0.0.0-20260909164052-59e4f4feb0dd
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/text v0.29.0
	modernc.org/sqlite v1.34.5
>>>>>>> feat/console-platform-roadmap
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
<<<<<<< HEAD
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace dynamic-runtime => github.com/ccb1900/gocordis v0.0.0-20260909154652-ffdf556ba9a8
=======
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.38.0 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
)

replace dynamic-runtime => github.com/ccb1900/gocordis v0.0.0-20260909164052-59e4f4feb0dd
>>>>>>> feat/console-platform-roadmap
