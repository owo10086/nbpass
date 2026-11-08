package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
)

func TestPasswordResetCommandReadsStdinAndImmediatelyRevokesOnlineSession(t *testing.T) {
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = panel.BcryptPasswords{Cost: 4} })
	c := h.Client()
	const oldPassword = "old-command-password-01"
	const newPassword = "new-command-password-02"
	c.Do("POST", "/api/auth/setup", map[string]string{"password": oldPassword}, 201)
	c.Do("POST", "/api/auth/login", map[string]string{"password": oldPassword}, 200)
	var output bytes.Buffer
	for _, input := range []string{"short\nshort\n", newPassword + "\nmismatch\n", newPassword} {
		if err := resetPassword(context.Background(), []string{"-data-dir", h.Dir}, passwordInput(t, input), &output); err == nil {
			t.Fatal("无效输入未被拒绝")
		}
		c.Do("GET", "/api/overview", nil, 200)
	}
	if err := resetPassword(context.Background(), []string{"-data-dir", h.Dir, newPassword}, passwordInput(t, ""), &output); err == nil {
		t.Fatal("不应接受密码参数")
	}
	if err := resetPassword(context.Background(), []string{"-data-dir", h.Dir}, passwordInput(t, newPassword+"\n"+newPassword+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), newPassword) {
		t.Fatal("命令输出泄露新密码")
	}
	c.Do("GET", "/api/overview", nil, 401)
	c.Do("POST", "/api/auth/login", map[string]string{"password": oldPassword}, 401)
	c.Do("POST", "/api/auth/login", map[string]string{"password": newPassword}, 200)
}

func passwordInput(t *testing.T, value string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}
