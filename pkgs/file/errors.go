package file

import "errors"

var (
	ErrFileConflict = errors.New("file conflict: identical file or filename already exists")
	ErrFileNotFound = errors.New("file not found")
	ErrAccessDenied = errors.New("access denied")
)
