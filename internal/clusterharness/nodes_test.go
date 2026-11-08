package clusterharness_test

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
)

func nodeBrowser(t *testing.T) (*clusterharness.Harness, *clusterharness.Client) {
	t.Helper()
	// 节点用例不测试密码工作因子；仍走真实 bcrypt，降低代价以便 race 验收。
	// 生产 cost=12 的物理断言和认证用例继续使用默认依赖。
	h := clusterharness.New(t, func(deps *panel.Dependencies) { deps.Passwords = nodePasswords{} })
	c := h.Client()
	setup(c)
	login(c)
	return h, c
}

type nodePasswords struct{}

func (nodePasswords) Hash(value string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.MinCost)
	return string(hash), err
}
func (nodePasswords) Verify(hash, value string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(value)) == nil
}

func TestInstallerAndNodeDownloadArePublicAndKeepTicketOutOfURLs(t *testing.T) {
	image := []byte("\x7fELF-test-node-artifact")
	h := clusterharness.New(t, func(deps *panel.Dependencies) {
		deps.NodeBinary = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(image)), nil }
	})
	c := h.Client()
	for path, contentType := range map[string]string{"/install-node.sh": "text/x-shellscript", "/downloads/nbpass-node-linux-amd64": "application/octet-stream"} {
		response := c.Request("GET", path, nil, nil)
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || len(data) == 0 || !strings.HasPrefix(response.Header.Get("Content-Type"), contentType) || (path == "/downloads/nbpass-node-linux-amd64" && !bytes.Equal(data, image)) {
			t.Fatalf("安装资源 %s: %d %s %v", path, response.StatusCode, data, err)
		}
	}
	setup(c)
	login(c)
	created := addNode(c, "安装节点")
	command := created["installation_command"].(string)
	if !strings.Contains(command, "/install-node.sh") || !strings.Contains(command, "-ticket '") || strings.Contains(command, "?ticket=") || strings.Contains(command, "--token") {
		t.Fatalf("安装命令: %s", command)
	}
}

func addNode(c *clusterharness.Client, name string) map[string]any {
	return c.Do("POST", "/api/nodes", map[string]string{"name": name}, 201)
}

func nodeRow(t *testing.T, c *clusterharness.Client, id string) map[string]any {
	t.Helper()
	for _, value := range c.Do("GET", "/api/nodes", nil, 200)["nodes"].([]any) {
		row := value.(map[string]any)
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("节点 %s 未出现在列表", id)
	return nil
}

// 缺少认证/同源门禁、命令泄露长期凭据或列表回显票据时失败。
func TestAddNodeReturnsOnlyShortLivedInstallationCredential(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	c.Do("POST", "/api/nodes", map[string]string{"name": "入口 A"}, 428)
	setup(c)
	c.Do("GET", "/api/nodes", nil, 401)
	login(c)
	response := c.Request("POST", "/api/nodes", map[string]string{"name": "入口 A"}, map[string]string{"Origin": "https://evil.example"})
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("跨源添加节点: %d", response.StatusCode)
	}
	c.Do("POST", "/api/nodes", map[string]string{"name": " "}, 400)
	created := addNode(c, "入口 A")
	ticket := created["installation_ticket"].(string)
	command := created["installation_command"].(string)
	if len(ticket) != 43 || !strings.Contains(command, ticket) || strings.Contains(command, "--token") {
		t.Fatalf("安装命令未只使用票据: %s", command)
	}
	expires, err := time.Parse(time.RFC3339, created["ticket_expires_at"].(string))
	if err != nil || !expires.Equal(h.Clock.Now().Add(30*time.Minute)) {
		t.Fatalf("票据期限: %v %v", expires, err)
	}
	row := nodeRow(t, c, created["id"].(string))
	if row["name"] != "入口 A" || row["desired_revision"] != float64(0) || row["applied_revision"] != float64(0) || row["config_status"] != "待确认" {
		t.Fatalf("新节点状态: %v", row)
	}
	if strings.Contains(fmt.Sprint(row), ticket) || row["token"] != nil {
		t.Fatal("列表泄露凭据")
	}
}
