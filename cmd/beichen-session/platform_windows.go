package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func openDefaultBrowser(address string) error {
	action, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	url, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, action, url, nil, nil, windows.SW_SHOWNORMAL)
}

func disableTerminalEcho(input *os.File) (func() error, error) {
	handle := windows.Handle(input.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(handle, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return nil, err
	}
	return func() error { return windows.SetConsoleMode(handle, mode) }, nil
}

func restrictExportPermissions(file *os.File) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(file.Name(), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func exportPermissionsNote() string {
	return "Windows 已限制导出文件供当前账号访问。可用 icacls deploy\\beichen-session.env 核对 ACL。"
}
