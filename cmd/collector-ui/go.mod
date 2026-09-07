module gocordis-csv-collector/desktop

go 1.24.0

require (
	github.com/wailsapp/wails/v2 v2.11.0+incompatible
	dynamic-runtime v0.0.0
	gocordis-csv-collector v0.0.0
)

replace gocordis-csv-collector => ../..

replace dynamic-runtime => ../../../go-cordis
