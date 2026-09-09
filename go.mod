module gocordis-csv-collector

go 1.24.0

require (
	dynamic-runtime v0.0.0-20260909004738-4d664e112a1c
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/text v0.29.0
)

require (
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	golang.org/x/sys v0.38.0 // indirect
)

replace dynamic-runtime => github.com/ccb1900/gocordis v0.0.0-20260909004738-4d664e112a1c
