package cmdenv

import (
	"fmt"

	files "gx/ipfs/QmXWZCd8jfaHmt4UDSnjKmGcrQMw95bDGWqEeVLVJjoANX/go-ipfs-files"
)

// GetFileArg returns the next file from the directory or an error
func GetFileArg(it files.DirIterator) (files.File, error) {
	if !it.Next() {
		err := it.Err()
		if err == nil {
			err = fmt.Errorf("expected a file argument")
		}
		return nil, err
	}
	file := files.FileFromEntry(it)
	if file == nil {
		return nil, fmt.Errorf("file argument was nil")
	}
	return file, nil
}
