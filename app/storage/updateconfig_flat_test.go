package storage

import (
	"encoding/json"
	"testing"

	"github.com/pigeonbox/core/conf"
)

func TestFlatUnmarshal(t *testing.T) {
	body := `{"type":"sftp","sftp":{"host":"127.0.0.1:2222","username":"tester","password":"testerpass","root":"upload"}}`
	req := &UpdateConfigRequest{}
	if err := json.Unmarshal([]byte(body), req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Type != "sftp" {
		t.Fatalf("Type=%q", req.Type)
	}
	if req.SFTP == nil || req.SFTP.Host != "127.0.0.1:2222" {
		t.Fatalf("SFTP 未填充: %+v", req.SFTP)
	}
	_ = conf.StorageConfig{}
	if !hasFlatStorageFields(req.StorageConfig) {
		t.Fatal("hasFlatStorageFields 应为 true")
	}
}
