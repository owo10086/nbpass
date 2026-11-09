// Package nodeupdate defines the narrow external interface of the independent updater.
// No caller can supply an executable path, command, download URL or state backup.
package nodeupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const Platform = "linux/amd64"
const InstallPath = "/usr/local/bin/nbpass-node"
const SocketPath = "/run/nbpass-update/control.sock"
const MaxSize = 128 << 20

var taskPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var ErrRolledBack = errors.New("新程序启动或业务自检失败，已回滚")

type Manifest struct {
	TaskID   string `json:"task_id"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

func (m Manifest) Validate() error {
	h, err := hex.DecodeString(m.SHA256)
	if !taskPattern.MatchString(m.TaskID) || !regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,128}$`).MatchString(m.Version) || m.Platform != Platform || m.Size <= 0 || m.Size > MaxSize || err != nil || len(h) != 32 || hex.EncodeToString(h) != m.SHA256 {
		return errors.New("升级清单的任务、版本、平台、大小或 SHA256 无效")
	}
	return nil
}
func Verify(m Manifest, data []byte) error {
	if err := m.Validate(); err != nil {
		return err
	}
	h := sha256.Sum256(data)
	if int64(len(data)) != m.Size || hex.EncodeToString(h[:]) != m.SHA256 {
		return errors.New("产物大小或 SHA256 不符")
	}
	return nil
}

type Progress struct {
	Manifest Manifest `json:"manifest"`
	Sequence uint64   `json:"sequence"`
	State    string   `json:"state"`
	Steps    []string `json:"steps"`
	Error    string   `json:"error,omitempty"`
}

func Terminal(state string) bool {
	return state == "ready" || state == "rolled_back" || state == "failed" || state == "unconfirmed"
}

type Service interface {
	Submit(context.Context, Manifest) (Progress, error)
	Status(context.Context) (Progress, error)
}

type Source interface {
	Manifest(context.Context, string) (Manifest, error)
	Artifact(context.Context, string) (io.ReadCloser, error)
}
type SourceFuncs struct {
	ManifestFunc func(context.Context, string) (Manifest, error)
	ArtifactFunc func(context.Context, string) (io.ReadCloser, error)
}

func (s SourceFuncs) Manifest(c context.Context, id string) (Manifest, error) {
	return s.ManifestFunc(c, id)
}
func (s SourceFuncs) Artifact(c context.Context, id string) (io.ReadCloser, error) {
	return s.ArtifactFunc(c, id)
}

type Apply func(context.Context, Manifest, []byte, func(string)) error

func Execute(ctx context.Context, wanted Manifest, source Source, apply Apply, emit func(string)) error {
	if err := wanted.Validate(); err != nil {
		return err
	}
	authoritative, err := source.Manifest(ctx, wanted.TaskID)
	if err != nil {
		return errors.New("无法通过 TLS 取得任务清单")
	}
	if authoritative != wanted {
		return errors.New("面板清单与升级任务绑定不符")
	}
	emit("downloading")
	image, err := source.Artifact(ctx, wanted.TaskID)
	if err != nil {
		return errors.New("无法下载固定节点产物")
	}
	defer image.Close()
	data, err := io.ReadAll(io.LimitReader(image, MaxSize+1))
	if err != nil {
		return errors.New("下载失败")
	}
	emit("verifying")
	if err = Verify(wanted, data); err != nil {
		return err
	}
	if apply == nil {
		return fmt.Errorf("更新服务缺少固定安装器")
	}
	return apply(ctx, wanted, data, emit)
}
