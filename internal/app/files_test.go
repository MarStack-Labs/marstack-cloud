package app

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type fileBody struct {
	ID        string   `json:"id"`
	FilePaths []string `json:"file_paths"`
	Files     []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	} `json:"files"`
}

type fileListBody struct {
	Instances []fileBody `json:"instances"`
}

func createWithFiles(t *testing.T, a *testApp, name, files string) *strings.Reader {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"container","image":"alpine:3.20","files":` +
		files + `}`
	return strings.NewReader(body)
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestAConfigFileReachesTheNodeAndNotTheOperator(t *testing.T) {
	a, nodeID := newSealingApp(t)

	files := `[{"path":"/etc/app.conf","content":"` + b64("secret=hunter2\n") + `","mode":"0640"}]`
	rec := do(t, a, http.MethodPost, "/v1/instances", createWithFiles(t, a, "web", files))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), b64("secret=hunter2\n")) {
		t.Fatal("the create response echoed the content back")
	}

	var created fileBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Join(created.FilePaths, ",") != "/etc/app.conf" {
		t.Fatalf("file_paths = %v, want the path an operator can see", created.FilePaths)
	}
	if len(created.Files) != 0 {
		t.Fatalf("files = %v, want the operator route never to serve content", created.Files)
	}

	placement(t, a, created.ID)

	nodeRec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/instances", nil)
	var list fileListBody
	if err := json.Unmarshal(nodeRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, in := range list.Instances {
		if in.ID != created.ID {
			continue
		}
		if len(in.Files) != 1 {
			t.Fatalf("the node saw %d files, want the one it has to write", len(in.Files))
		}
		if in.Files[0].Content != b64("secret=hunter2\n") || in.Files[0].Mode != "0640" {
			t.Fatalf("the node saw %+v, want the content and mode as given", in.Files[0])
		}
		return
	}
	t.Fatal("the instance is not in the node's view")
}

func TestAFileIsRefusedWithNoKeyToSealItWith(t *testing.T) {
	a, _ := newBalancingApp(t)

	files := `[{"path":"/etc/app.conf","content":"` + b64("x") + `"}]`
	rec := do(t, a, http.MethodPost, "/v1/instances", createWithFiles(t, a, "web", files))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestABadFileIsRefused(t *testing.T) {
	a, _ := newSealingApp(t)

	cases := map[string]string{
		"relative path":  `[{"path":"etc/app.conf","content":"` + b64("x") + `"}]`,
		"climbing out":   `[{"path":"/etc/../../escape","content":"` + b64("x") + `"}]`,
		"a dot segment":  `[{"path":"/etc/./app.conf","content":"` + b64("x") + `"}]`,
		"not base64":     `[{"path":"/etc/app.conf","content":"not base64!!"}]`,
		"a silly mode":   `[{"path":"/etc/app.conf","content":"` + b64("x") + `","mode":"7777"}]`,
		"a decimal mode": `[{"path":"/etc/app.conf","content":"` + b64("x") + `","mode":"999"}]`,
		"the same path twice": `[{"path":"/a","content":"` + b64("x") + `"},` +
			`{"path":"/a","content":"` + b64("y") + `"}]`,
	}

	for why, files := range cases {
		rec := do(t, a, http.MethodPost, "/v1/instances", createWithFiles(t, a, "bad", files))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d (%s)",
				why, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	}
}

func TestFileContentIsNotOnDiskInTheClear(t *testing.T) {
	a, dir := sealingAppIn(t, schemaDir(t))
	registerNode(t, a, "bm-1", "rack-a")

	files := `[{"path":"/etc/app.conf","content":"` +
		b64("password=qq7-unique-file-marker\n") + `"}]`
	rec := do(t, a, http.MethodPost, "/v1/instances", createWithFiles(t, a, "web", files))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	onDisk := everythingUnder(t, dir)
	if strings.Contains(onDisk, "qq7-unique-file-marker") ||
		strings.Contains(onDisk, b64("password=qq7-unique-file-marker\n")) {
		t.Fatal("the content is on the control plane disk in the clear")
	}
	if !strings.Contains(onDisk, "/etc/app.conf") {
		t.Fatal("the path is not stored, so listing paths would need the key")
	}
}
