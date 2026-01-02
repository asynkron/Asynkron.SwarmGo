package clipboard

// Minimal stub to satisfy bubbles/textarea clipboard dependency without
// external system access. Clipboard data is kept in-process only.
var data string

// WriteAll stores text in an in-memory clipboard.
func WriteAll(text string) error {
	data = text
	return nil
}

// ReadAll returns the in-memory clipboard contents.
func ReadAll() (string, error) {
	return data, nil
}
