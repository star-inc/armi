package file

import "errors"

var (
	ErrFileConflict = errors.New("file conflict: identical file or filename already exists")
	ErrFileNotFound = errors.New("file not found")
	ErrAccessDenied = errors.New("access denied")
)

type ConflictError struct {
	ConflictingFileID   string
	ConflictingFileHash string
}

func (e *ConflictError) Error() string {
	return ErrFileConflict.Error()
}

func (e *ConflictError) Unwrap() error {
	return ErrFileConflict
}
