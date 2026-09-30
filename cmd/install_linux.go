package cmd

import (
	"fmt"
	"os"
	stdexec "os/exec"
	"strings"

	"github.com/InazumaV/V2bX/common/exec"
	"github.com/spf13/cobra"
)

var targetVersion string

var (
	updateCommand = cobra.Command{
		Use:   "update",
		Short: "Update V2bX version",
		RunE: func(_ *cobra.Command, _ []string) error {
			installer, err := os.CreateTemp("", "V2bX-install-*.sh")
			if err != nil {
				return err
			}
			path := installer.Name()
			defer os.Remove(path)
			if err := installer.Close(); err != nil {
				return err
			}
			download := stdexec.Command("curl", "-fL", "--retry", "3",
				"https://github.com/Laosixok/V2BX-malio/releases/latest/download/install.sh", "-o", path)
			download.Stdout, download.Stderr = os.Stdout, os.Stderr
			if err := download.Run(); err != nil {
				return fmt.Errorf("download installer: %w", err)
			}
			install := stdexec.Command("bash", path, targetVersion)
			install.Stdin, install.Stdout, install.Stderr = os.Stdin, os.Stdout, os.Stderr
			return install.Run()
		},
		Args: cobra.NoArgs,
	}
	uninstallCommand = cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall V2bX",
		Run:   uninstallHandle,
	}
)

func init() {
	updateCommand.PersistentFlags().StringVar(&targetVersion, "version", "", "update target version")
	command.AddCommand(&updateCommand)
	command.AddCommand(&uninstallCommand)
}

func uninstallHandle(_ *cobra.Command, _ []string) {
	var yes string
	fmt.Println(Warn("确定要卸载 V2bX 吗?(Y/n)"))
	fmt.Scan(&yes)
	if strings.ToLower(yes) != "y" {
		fmt.Println("已取消卸载")
	}
	_, err := exec.RunCommandByShell("systemctl stop V2bX&&systemctl disable V2bX")
	if err != nil {
		fmt.Println(Err("exec cmd error: ", err))
		fmt.Println(Err("卸载失败"))
		return
	}
	_ = os.RemoveAll("/etc/systemd/system/V2bX.service")
	_ = os.RemoveAll("/etc/V2bX/")
	_ = os.RemoveAll("/usr/local/V2bX/")
	_ = os.RemoveAll("/bin/V2bX")
	_, err = exec.RunCommandByShell("systemctl daemon-reload&&systemctl reset-failed")
	if err != nil {
		fmt.Println(Err("exec cmd error: ", err))
		fmt.Println(Err("卸载失败"))
		return
	}
	fmt.Println(Ok("卸载成功"))
}
