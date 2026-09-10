module gocordis-csv-collector

go 1.25.0

require (
	dynamic-runtime v0.0.0-20260910153359-f8448a571b5f
	github.com/fsnotify/fsnotify v1.10.1
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/text v0.29.0
	modernc.org/sqlite v1.34.5
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.38.0 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
)

replace dynamic-runtime => github.com/ccb1900/gocordis v0.0.0-20260910153359-f8448a571b5f
