package workspace

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// renameFile operates on single-component names in a pinned parent directory.
// Unlike os.Rename on Go 1.24, it never re-resolves an absolute filesystem path.
func renameFile(root *os.Root, oldName, newName string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	objectName, err := windows.NewNTUnicodeString(oldName)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(directory.Fd()), ObjectName: objectName, Attributes: windows.OBJ_DONT_REPARSE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var status windows.IO_STATUS_BLOCK
	var file windows.Handle
	err = windows.NtCreateFile(&file, windows.DELETE|windows.SYNCHRONIZE, &attributes, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(file)
	name, err := windows.UTF16FromString(newName)
	if err != nil {
		return err
	}
	type renameInfo struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [1]uint16
	}
	var layout renameInfo
	nameBytes := (len(name) - 1) * 2
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+nameBytes)
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS
	info.RootDirectory = windows.Handle(directory.Fd())
	info.FileNameLength = uint32(nameBytes)
	copy(unsafe.Slice(&info.FileName[0], len(name)-1), name[:len(name)-1])
	err = windows.NtSetInformationFile(file, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
	runtime.KeepAlive(directory)
	return err
}
