//go:build windows

package utils

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func CreateSymlink(src string, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return err
		}
	}

	err := os.Link(src, dst)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		err = os.Rename(src, dst)
		if err != nil {
			return err
		}
	}
	return nil
}

func GetAvailableDiskSpace(path string) (uint64, error) {
	// On Windows, we can use the GetDiskFreeSpaceEx and mac function to get the available space
	// https://docs.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getdiskfreespaceex
	var freeBytesAvailable uint64

	err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(path), nil, &freeBytesAvailable, nil)
	if err != nil {
		return 0, err
	}
	return freeBytesAvailable, nil
}
