package pluginmeta

// Reset clears every registration. Test-only: exported through
// pluginmeta_export_test.go so production builds never see it.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	types = map[string]TypeInfo{}
	packages = nil
}
