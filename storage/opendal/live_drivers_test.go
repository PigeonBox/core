//go:build live

// 存储驱动真机集成测试（需本地 Docker 起真实 FTP/SFTP 服务，CI 跳过）：
//
//	docker run -d --name fcb-sftp -p 2222:22 -e SFTP_USERS=tester:testerpass:1001 atmoz/sftp:alpine
//	docker run -d --name fcb-ftp -p 2121:21 -p 21000-21010:21000-21010 \
//	  -e FTP_USER_NAME=tester -e FTP_USER_PASS=testerpass \
//	  -e PASV_ADDRESS=127.0.0.1 -e PASV_MIN_PORT=21000 -e PASV_MAX_PORT=21010 \
//	  stilliard/pure-ftpd:hardened
//
//	go test -tags live ./storage/opendal/ -run Live -v
package opendal

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func liveExercise(t *testing.T, op *Operator) {
	t.Helper()
	ctx := context.Background()

	if err := Probe(ctx, op); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	// Write/Read 往返
	body := []byte("live-driver-check-9527")
	if err := op.Write(ctx, "live/dir-a/file1.txt", body); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := op.Read(ctx, "live/dir-a/file1.txt")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Read roundtrip: %v %q", err, got)
	}

	// Stat
	md, err := op.Stat(ctx, "live/dir-a/file1.txt")
	if err != nil || md.Size != int64(len(body)) || md.IsDir {
		t.Fatalf("Stat: %v %+v", err, md)
	}

	// 不存在 → os.ErrNotExist 语义
	if _, err := op.Stat(ctx, "live/dir-a/missing.txt"); err == nil {
		t.Fatal("Stat missing 应报错")
	}

	// List
	entries, err := op.List(ctx, "live/dir-a")
	if err != nil || len(entries) != 1 || entries[0].Path != "file1.txt" {
		t.Fatalf("List: %v %+v", err, entries)
	}

	// 大于一个缓冲区的流式写入（WriteStream）
	big := strings.Repeat("B", 300*1024)
	if err := op.WriteStream(ctx, "live/dir-a/big.bin", strings.NewReader(big), int64(len(big))); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}
	bmd, err := op.Stat(ctx, "live/dir-a/big.bin")
	if err != nil || bmd.Size != int64(len(big)) {
		t.Fatalf("WriteStream Stat: %v %+v", err, bmd)
	}

	// 清理：RemoveAll 整个 live 前缀
	if err := op.RemoveAll(ctx, "live"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if op.Exists(ctx, "live/dir-a/file1.txt") {
		t.Fatal("RemoveAll 后仍存在")
	}
}

func TestLiveSFTP(t *testing.T) {
	op, err := New(Config{
		Scheme: SchemeSFTP,
		Options: map[string]string{
			"host":     "127.0.0.1:2222",
			"username": "tester",
			"password": "testerpass",
			"root":     "upload",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	liveExercise(t, op)
}

func TestLiveFTP(t *testing.T) {
	op, err := New(Config{
		Scheme: SchemeFTP,
		Options: map[string]string{
			"host":     "127.0.0.1:2121",
			"username": "tester",
			"password": "testerpass",
			"root":     "",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	liveExercise(t, op)
}
