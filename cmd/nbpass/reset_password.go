package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"nbpass/internal/panel"
	"nbpass/internal/platform"
)

func resetPassword(ctx context.Context, args []string, input *os.File, output io.Writer) error {
	flags := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	flags.SetOutput(output)
	dir := flags.String("data-dir", "data", "面板现有数据库的专用目录")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("密码只能从标准输入读取，不接受命令参数")
	}
	path := filepath.Join(*dir, "panel.db")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("请指定已初始化面板的数据目录: %w", err)
	}
	reader := bufio.NewReaderSize(input, 256)
	read := func(prompt string) (string, error) {
		fmt.Fprint(output, prompt)
		if term.IsTerminal(int(input.Fd())) {
			value, err := term.ReadPassword(int(input.Fd()))
			fmt.Fprintln(output)
			return string(value), err
		}
		value, err := reader.ReadSlice('\n')
		if err != nil {
			return "", errors.New("标准输入须提供两行密码，每行以换行符结束")
		}
		return strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r"), nil
	}
	password, err := read("新管理员密码（12–72 字节）: ")
	if err != nil {
		return err
	}
	confirmation, err := read("再次输入新密码: ")
	if err != nil {
		return err
	}
	if password != confirmation {
		return errors.New("两次密码不一致")
	}
	if err := panel.ResetAdministratorPassword(ctx, password, panel.Dependencies{
		Persistence: platform.SQLite{Path: path},
	}); err != nil {
		return fmt.Errorf("重置管理员密码: %w", err)
	}
	fmt.Fprintln(output, "管理员密码已重置，全部登录会话及登录锁定已清除。请使用新密码登录。")
	return nil
}
